package logutils

import (
	"bytes"
	"fmt"
	"os"
	"path"
	"runtime"
	"sort"
	"strings"

	log "github.com/sirupsen/logrus"
)

const (
	// FieldForceFlush is a field name used to signal to the BackgroundHook that it should flush the log after this
	// message.  It can be used as follows: logrus.WithField(FieldForceFlush, true).Info("...")
	FieldForceFlush = "__flush__"

	// fieldFileName is a reserved field name used to pass the filename from the ContextHook to our Formatter.
	fieldFileName = "__file__"
	// fieldLineNumber is a reserved field name used to pass the line number from the ContextHook to our Formatter.
	fieldLineNumber = "__line__"
)

// Formatter is our custom log formatter designed to balance ease of machine processing
// with human readability.  Logs include:
//    - A sortable millisecond timestamp, for scanning and correlating logs
//    - The log level, near the beginning of the line, to aid in visual scanning
//    - The PID of the process to make it easier to spot log discontinuities (If
//      you are looking at two disjoint chunks of log, were they written by the
//      same process?  Was there a restart in-between?)
//    - The file name and line number, as essential context
//    - The message!
//    - Log fields appended in sorted order
//
// Example:
//    2017-01-05 09:17:48.238 [INFO][85386] endpoint_mgr.go 434: Skipping configuration of
//    interface because it is oper down. ifaceName="cali1234"
type Formatter struct{}

// Format function
func (f *Formatter) Format(entry *log.Entry) ([]byte, error) {
	stamp := entry.Time.Format("2006-01-02 15:04:05.000")
	levelStr := strings.ToUpper(entry.Level.String())
	pid := os.Getpid()
	fileName := entry.Data[fieldFileName]
	lineNo := entry.Data[fieldLineNumber]
	b := entry.Buffer
	if b == nil {
		b = &bytes.Buffer{}
	}
	fmt.Fprintf(b, "%s [%s][%d] %v %v: %v", stamp, levelStr, pid, fileName, lineNo, entry.Message)
	appendKVsAndNewLine(b, entry)
	return b.Bytes(), nil
}

// appendKeysAndNewLine writes the KV pairs attached to the entry to the end of the buffer, then
// finishes it with a newline.
func appendKVsAndNewLine(b *bytes.Buffer, entry *log.Entry) {
	// Sort the keys for consistent output.
	var keys []string = make([]string, 0, len(entry.Data))
	for k := range entry.Data {
		keys = append(keys, k)
	}
	sort.Strings(keys)

	for _, key := range keys {
		if key == fieldFileName || key == fieldLineNumber || key == FieldForceFlush {
			continue
		}
		var value interface{} = entry.Data[key]
		var stringifiedValue string
		if err, ok := value.(error); ok {
			stringifiedValue = err.Error()
		} else if stringer, ok := value.(fmt.Stringer); ok {
			// Trust the value's String() method.
			stringifiedValue = stringer.String()
		} else {
			// No string method, use %#v to get a more thorough dump.
			fmt.Fprintf(b, " %v=%#v", key, value)
			continue
		}
		b.WriteByte(' ')
		b.WriteString(key)
		b.WriteByte('=')
		b.WriteString(stringifiedValue)
	}
	b.WriteByte('\n')
}

// NullWriter is a dummy writer that always succeeds and does nothing.
type NullWriter struct{}

func (w *NullWriter) Write(p []byte) (int, error) {
	return len(p), nil
}

// ContextHook provides ctxt
type ContextHook struct {
}

// Levels in ctxt
func (hook ContextHook) Levels() []log.Level {
	return log.AllLevels
}

// Fire in ctxt
func (hook ContextHook) Fire(entry *log.Entry) error {
	// We used to do runtime.Callers(6, pcs) here so that we'd skip straight to the expected
	// frame.  However, if an intermediate frame gets inlined we can skip too many frames in
	// that case.  The only safe option is to use skip=1 and then let CallersFrames() deal
	// with any inlining.
	pcs := make([]uintptr, 10)
	if numEntries := runtime.Callers(1, pcs); numEntries > 0 {
		pcs = pcs[:numEntries]
		frames := runtime.CallersFrames(pcs)
		for {
			frame, more := frames.Next()
			if !shouldSkipFrame(frame) {
				// We found the frame we were looking for.  Record its file/line number.
				entry.Data[fieldFileName] = path.Base(frame.File)
				entry.Data[fieldLineNumber] = frame.Line
				break
			}
			if !more {
				break
			}
		}
	}
	return nil
}

// shouldSkipFrame returns true if the given frame belongs to the logging library (or this utility package).
// Note: this is on the critical path for every log, if you need to update it, make sure to run the
// benchmarks.
//
// Some things we've tried that were worse than strings.HasSuffix():
//
// - using a regexp:            ~100x slower
// - using strings.LastIndex(): ~10x slower
// - omitting the package:      no benefit
func shouldSkipFrame(frame runtime.Frame) bool {
	return strings.HasSuffix(frame.File, "henderiw/kubemon2/lib/logutils/logutils.go") ||
		strings.HasSuffix(frame.File, "sirupsen/logrus/hooks.go") ||
		strings.HasSuffix(frame.File, "logrus/hooks.go") ||
		strings.HasSuffix(frame.File, "sirupsen/logrus/entry.go") ||
		strings.HasSuffix(frame.File, "sirupsen/logrus/logger.go") ||
		strings.HasSuffix(frame.File, "sirupsen/logrus/exported.go")
}
