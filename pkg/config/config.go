package config

import "github.com/kelseyhightower/envconfig"

// Config provides configuration structure
type Config struct {
	// Minimum log level to emit.
	LogLevel string `default:"info" split_words:"true"`

	// Period to perform reconciliation with the datastore.
	ReconcilerPeriod string `default:"5m" split_words:"true"`

	// Which controllers to run.
	EnabledControllers string `default:"node,policy,namespace,workloadendpoint,serviceaccount" split_words:"true"`

	// Number of workers to run for each controller.
	WorkloadEndpointWorkers int `default:"1" split_words:"true"`
	ProfileWorkers          int `default:"1" split_words:"true"`
	PolicyWorkers           int `default:"1" split_words:"true"`
	NodeWorkers             int `default:"1" split_words:"true"`

	// Path to a kubeconfig file to use for accessing the k8s API.
	Kubeconfig string `default:"" split_words:"false"`

	// Enable healthchecks
	HealthEnabled bool `default:"true"`

	// Enable syncing of node labels
	SyncNodeLabels bool `default:"true" split_words:"true"`
}

// Parse parses envconfig and stores in Config struct
func (c *Config) Parse() error {
	return envconfig.Process("", c)
}
