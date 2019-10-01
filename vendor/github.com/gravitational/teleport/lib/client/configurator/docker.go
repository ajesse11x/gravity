// +build !linux

/*
Copyright 2019 Gravitational, Inc.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package configurator

type dockerConfigurator struct{}

// Configure is no-op for Docker configurator on non-Linux systems.
func (c *dockerConfigurator) Configure(config Config) error {
	return nil
}

// IsConfigured is no-op for Docker configurator on non-Linux systems.
func (c *dockerConfigurator) IsConfigured(config Config) (bool, error) {
	return false, nil
}
