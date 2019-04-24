/*
Copyright 2018 Gravitational, Inc.

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

package cli

import (
	"sort"
	"strings"
	"time"

	"github.com/gravitational/gravity/lib/fsm"
	"github.com/gravitational/gravity/lib/localenv"
	"github.com/gravitational/gravity/lib/ops"
	"github.com/gravitational/gravity/lib/storage"

	"github.com/gravitational/trace"
	"github.com/sirupsen/logrus"
)

// PhaseParams is a set of parameters for a single phase execution
type PhaseParams struct {
	// PhaseID is the ID of the phase to execute
	PhaseID string
	// OperationID specifies the operation to work with.
	// If unspecified, last operation is used.
	// Some commands will require the last operation to also be active
	OperationID string
	// Force allows to force phase execution
	Force bool
	// Timeout is phase execution timeout
	Timeout time.Duration
	// SkipVersionCheck overrides the verification of binary version compatibility
	SkipVersionCheck bool
	// Installer specifies the installer to manage installation-specific phases.
	// If unspecified defaults to an instance of installer
	Installer Installer
}

func ResumeOperation(localEnv, updateEnv, joinEnv *localenv.LocalEnvironment, params PhaseParams) error {
	if params.Installer == nil {
		params.Installer = defaultInstaller{}
	}
	params.PhaseID = fsm.RootPhase
	err := ExecutePhase(localEnv, updateEnv, joinEnv, params)
	if err == nil {
		return nil
	}
	if err != nil && !trace.IsNotFound(err) {
		return trace.Wrap(err)
	}
	// No operation found.
	// Attempt to restart the installation
	return trace.Wrap(params.Installer.Restart(localEnv))
}

func ExecutePhase(localEnv, updateEnv, joinEnv *localenv.LocalEnvironment, params PhaseParams) error {
	op, err := getActiveOperation(localEnv, updateEnv, joinEnv, params.OperationID)
	if err != nil {
		return trace.Wrap(err)
	}
	switch op.Type {
	case ops.OperationInstall:
		installer := params.Installer
		if params.Installer == nil {
			installer = defaultInstaller{}
		}
		return installer.ExecutePhase(localEnv, params, op)
	case ops.OperationExpand:
		return executeJoinPhase(localEnv, joinEnv, params, op)
	case ops.OperationUpdate:
		return executeUpdatePhase(localEnv, updateEnv, params, *op)
	case ops.OperationUpdateRuntimeEnviron:
		return executeEnvironPhase(localEnv, updateEnv, params, *op)
	case ops.OperationUpdateConfig:
		return executeConfigPhase(localEnv, updateEnv, params, *op)
	case ops.OperationGarbageCollect:
		return executeGarbageCollectPhase(localEnv, params, op)
	default:
		return trace.BadParameter("operation type %q does not support plan execution", op.Type)
	}
}

func RollbackPhase(localEnv, updateEnv, joinEnv *localenv.LocalEnvironment, params PhaseParams) error {
	op, err := getActiveOperation(localEnv, updateEnv, joinEnv, params.OperationID)
	if err != nil {
		return trace.Wrap(err)
	}
	switch op.Type {
	case ops.OperationInstall:
		installer := params.Installer
		if params.Installer == nil {
			installer = defaultInstaller{}
		}
		return installer.RollbackPhase(localEnv, params, op)
	case ops.OperationExpand:
		return rollbackJoinPhase(localEnv, joinEnv, params, op)
	case ops.OperationUpdate:
		return rollbackUpdatePhase(localEnv, updateEnv, params, *op)
	case ops.OperationUpdateRuntimeEnviron:
		return rollbackEnvironPhase(localEnv, updateEnv, params, *op)
	case ops.OperationUpdateConfig:
		return rollbackConfigPhase(localEnv, updateEnv, params, *op)
	default:
		return trace.BadParameter("operation type %q does not support plan rollback", op.Type)
	}
}

func completeOperationPlan(localEnv, updateEnv, joinEnv *localenv.LocalEnvironment, operationID string) error {
	op, err := getActiveOperation(localEnv, updateEnv, joinEnv, operationID)
	if err != nil {
		return trace.Wrap(err)
	}
	switch op.Type {
	case ops.OperationInstall:
		// There's only one install operation
		return completeInstallPlan(localEnv, op)
	case ops.OperationExpand:
		return completeJoinPlan(localEnv, joinEnv, op)
	case ops.OperationUpdate:
		return completeUpdatePlan(localEnv, updateEnv, *op)
	case ops.OperationUpdateRuntimeEnviron:
		return completeEnvironPlan(localEnv, updateEnv, *op)
	case ops.OperationUpdateConfig:
		return completeConfigPlan(localEnv, updateEnv, *op)
	default:
		return trace.BadParameter("operation type %q does not support plan completion", op.Type)
	}
}

func getLastOperation(localEnv, updateEnv, joinEnv *localenv.LocalEnvironment, operationID string) (*ops.SiteOperation, error) {
	operations, err := getBackendOperations(localEnv, updateEnv, joinEnv, operationID)
	if err != nil {
		return nil, trace.Wrap(err)
	}
	log.WithField("operations", oplist(operations).String()).Debug("Fetched backend operations.")
	if len(operations) == 0 {
		if operationID != "" {
			return nil, trace.NotFound("no operation with ID %v found", operationID)
		}
		return nil, trace.NotFound("no operation found")
	}
	if len(operations) == 1 && operationID != "" {
		log.WithField("operation", operations[0]).Debug("Fetched operation by ID.")
		return &operations[0], nil
	}
	if len(operations) != 1 {
		log.Infof("Multiple operations found: \n%v\n, please specify operation with --operation-id.\n"+
			"Displaying the most recent operation.",
			oplist(operations))
	}
	return &operations[0], nil
}

func getActiveOperation(localEnv, updateEnv, joinEnv *localenv.LocalEnvironment, operationID string) (*ops.SiteOperation, error) {
	operations, err := getBackendOperations(localEnv, updateEnv, joinEnv, operationID)
	if err != nil {
		return nil, trace.Wrap(err)
	}
	log.WithField("operations", oplist(operations).String()).Debug("Fetched backend operations.")
	if len(operations) == 0 {
		if operationID != "" {
			return nil, trace.NotFound("no operation with ID %v found", operationID)
		}
		return nil, trace.NotFound("no operation found")
	}
	op, err := getActiveOperationFromList(operations)
	if err != nil {
		return nil, trace.Wrap(err)
	}
	return op, nil
}

// getBackendOperations returns the list of operation from the specified backends
// in descending order (sorted by creation time)
func getBackendOperations(localEnv, updateEnv, joinEnv *localenv.LocalEnvironment, operationID string) (result []ops.SiteOperation, err error) {
	b := newBackendOperations()
	err = b.List(localEnv, updateEnv, joinEnv)
	if err != nil {
		return nil, trace.Wrap(err)
	}
	for _, op := range b.operations {
		if operationID == "" || operationID == op.ID {
			result = append(result, op)
		}
	}
	sort.Slice(result, func(i, j int) bool {
		return result[i].Created.After(result[j].Created)
	})
	return result, nil
}

func newBackendOperations() backendOperations {
	return backendOperations{
		operations: make(map[string]ops.SiteOperation),
	}
}

func (r *backendOperations) List(localEnv, updateEnv, joinEnv *localenv.LocalEnvironment) error {
	clusterEnv, err := localEnv.NewClusterEnvironment(localenv.WithEtcdTimeout(1 * time.Second))
	if err != nil {
		log.WithError(err).Debug("Failed to create cluster environment.")
	}
	if clusterEnv != nil {
		err = r.init(clusterEnv.Backend)
		if err != nil {
			log.WithError(err).Debug("Failed to query cluster operations.")
		}
	}
	if updateEnv != nil {
		r.getOperationAndUpdateCache(getOperationFromBackend(updateEnv.Backend),
			log.WithField("context", "update"))
	}
	if joinEnv != nil {
		r.getOperationAndUpdateCache(getOperationFromBackend(joinEnv.Backend),
			log.WithField("context", "expand"))
	}
	// Only fetch operation from remote (install) environment if the install operation is ongoing
	// or we failed to fetch the operation details from the cluster
	if r.isActiveInstallOperation() {
		wizardEnv, err := localenv.NewRemoteEnvironment()
		if err == nil && wizardEnv.Operator != nil {
			cluster, err := wizardEnv.Operator.GetLocalSite()
			if err == nil {
				r.getOperationAndUpdateCache(getOperationFromOperator(wizardEnv.Operator, cluster.Key()),
					log.WithField("context", "install"))
				return nil
			}
		}
		log.WithError(err).Warn("Failed to comnect to wizard.")
		wizardLocalEnv, err := localEnv.NewLocalWizardEnvironment()
		if err != nil {
			return trace.Wrap(err, "failed to read local wizard environment")
		}
		r.getOperationAndUpdateCache(getOperationFromBackend(wizardLocalEnv.Backend),
			log.WithField("context", "install"))

	}
	return nil
}

func (r *backendOperations) init(clusterBackend storage.Backend) error {
	clusterOperations, err := storage.GetOperations(clusterBackend)
	if err != nil {
		return trace.Wrap(err, "failed to query cluster operations")
	}
	if len(clusterOperations) == 0 {
		return nil
	}
	// Initialize the operation state from the list of existing cluster operations
	for _, op := range clusterOperations {
		r.operations[op.ID] = (ops.SiteOperation)(op)
	}
	r.clusterOperation = (*ops.SiteOperation)(&clusterOperations[0])
	r.operations[r.clusterOperation.ID] = *r.clusterOperation
	return nil
}

func (r *backendOperations) getOperationAndUpdateCache(getter operationGetter, logger logrus.FieldLogger) *ops.SiteOperation {
	op, err := getter.getOperation()
	if err == nil {
		// Operation from the backend takes precedence over the existing operation (from cluster state)
		r.operations[op.ID] = (ops.SiteOperation)(*op)
	} else {
		logger.WithError(err).Warn("Failed to query operation.")
	}
	return (*ops.SiteOperation)(op)
}

func (r backendOperations) isActiveInstallOperation() bool {
	// FIXME: continue using wizard as source of truth as operation state
	// replicated in etcd is reported completed before it actually is
	return r.clusterOperation == nil || (r.clusterOperation.Type == ops.OperationInstall)
}

type backendOperations struct {
	operations       map[string]ops.SiteOperation
	clusterOperation *ops.SiteOperation
}

func getActiveOperationFromList(operations []ops.SiteOperation) (*ops.SiteOperation, error) {
	for _, op := range operations {
		if !op.IsCompleted() {
			return &op, nil
		}
	}
	return nil, trace.NotFound("no active operations found")
}

func isActiveOperation(op ops.SiteOperation) bool {
	return op.IsFailed() || !op.IsCompleted()
}

func (r oplist) String() string {
	var ops []string
	for _, op := range r {
		ops = append(ops, op.String())
	}
	return strings.Join(ops, "\n")
}

type oplist []ops.SiteOperation

func getOperationFromOperator(operator ops.Operator, clusterKey ops.SiteKey) operationGetter {
	return operationGetterFunc(func() (*ops.SiteOperation, error) {
		op, _, err := ops.GetLastOperation(clusterKey, operator)
		if err != nil {
			return nil, trace.Wrap(err)
		}
		return op, nil
	})
}

func getOperationFromBackend(backend storage.Backend) operationGetter {
	return operationGetterFunc(func() (*ops.SiteOperation, error) {
		op, err := storage.GetLastOperation(backend)
		if err != nil {
			return nil, trace.Wrap(err)
		}
		return (*ops.SiteOperation)(op), nil
	})
}

func (r operationGetterFunc) getOperation() (*ops.SiteOperation, error) {
	return r()
}

type operationGetterFunc func() (*ops.SiteOperation, error)

type operationGetter interface {
	getOperation() (*ops.SiteOperation, error)
}

func (defaultInstaller) ExecutePhase(localEnv *localenv.LocalEnvironment, p PhaseParams, operation *ops.SiteOperation) error {
	return trace.Wrap(executeInstallPhase(localEnv, p, operation))
}

func (defaultInstaller) RollbackPhase(localEnv *localenv.LocalEnvironment, p PhaseParams, operation *ops.SiteOperation) error {
	return trace.Wrap(rollbackInstallPhase(localEnv, p, operation))
}

func (defaultInstaller) Restart(localEnv *localenv.LocalEnvironment) error {
	return trace.Wrap(startInstall(localEnv, NewDefaultInstallConfig()))
}

type defaultInstaller struct{}

// Installer manages installation-specific tasks
type Installer interface {
	// ExecutePhase executes an installation phase specified with params
	ExecutePhase(*localenv.LocalEnvironment, PhaseParams, *ops.SiteOperation) error
	// RollbackPhase rolls back an installation phase specified with params
	RollbackPhase(*localenv.LocalEnvironment, PhaseParams, *ops.SiteOperation) error
	// Restart restarts the installation with default parameters
	Restart(*localenv.LocalEnvironment) error
}
