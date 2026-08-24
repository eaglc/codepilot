package app

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/eaglc/codepilot/internal/codingagent"
	codingfile "github.com/eaglc/codepilot/internal/codingstore/file"
	sessionfile "github.com/eaglc/codepilot/internal/sessionstore/file"
)

// MaintenanceOptions identifies the durable StateDir inspected by doctor/repair.
type MaintenanceOptions struct{ StateDir string }

// DiagnoseState assembles a coherent, read-only consistency report while excluding writers.
func DiagnoseState(ctx context.Context, options MaintenanceOptions) (codingagent.ConsistencyReport, error) {
	stateDir, err := maintenanceStateDir(options.StateDir)
	if err != nil {
		return codingagent.ConsistencyReport{}, err
	}
	if _, err := os.Stat(stateDir); errors.Is(err, os.ErrNotExist) {
		return codingagent.ConsistencyReport{GeneratedAt: time.Now().UTC()}, nil
	} else if err != nil {
		return codingagent.ConsistencyReport{}, fmt.Errorf("diagnose CodePilot state: %w", err)
	}
	lease, err := sessionfile.AcquireStateInspectionLease(stateDir)
	if err != nil {
		return codingagent.ConsistencyReport{}, err
	}
	report, diagnoseErr := diagnoseExistingState(ctx, stateDir)
	closeErr := lease.Close()
	return report, errors.Join(diagnoseErr, closeErr)
}

// RepairState explicitly reconciles recoverable transactions and archives broken
// bindings. It never removes a session directory, journal, or worktree record.
func RepairState(ctx context.Context, options MaintenanceOptions) (codingagent.ConsistencyRepairReport, error) {
	stateDir, err := maintenanceStateDir(options.StateDir)
	if err != nil {
		return codingagent.ConsistencyRepairReport{}, err
	}
	stateDir, err = prepareDirectory(stateDir)
	if err != nil {
		return codingagent.ConsistencyRepairReport{}, fmt.Errorf("repair CodePilot state: %w", err)
	}
	lease, err := sessionfile.AcquireStateLease(stateDir)
	if err != nil {
		return codingagent.ConsistencyRepairReport{}, err
	}
	products, err := codingfile.NewRepository(stateDir)
	if err != nil {
		_ = lease.Close()
		return codingagent.ConsistencyRepairReport{}, err
	}
	agents, err := sessionfile.NewRepository(stateDir)
	if err != nil {
		_ = lease.Close()
		return codingagent.ConsistencyRepairReport{}, err
	}
	manager, err := codingagent.NewConsistencyManager(codingagent.ConsistencyDependencies{Sessions: products, AgentSessions: agents, Worktrees: products})
	if err != nil {
		_ = lease.Close()
		return codingagent.ConsistencyRepairReport{}, err
	}
	report, repairErr := manager.Repair(ctx)
	closeErr := lease.Close()
	return report, errors.Join(repairErr, closeErr)
}

func diagnoseExistingState(ctx context.Context, stateDir string) (codingagent.ConsistencyReport, error) {
	products, err := codingfile.OpenRepository(stateDir)
	if err != nil {
		return codingagent.ConsistencyReport{}, err
	}
	agents, err := sessionfile.OpenRepository(stateDir)
	if err != nil {
		return codingagent.ConsistencyReport{}, err
	}
	manager, err := codingagent.NewConsistencyManager(codingagent.ConsistencyDependencies{Sessions: products, AgentSessions: agents, Worktrees: products})
	if err != nil {
		return codingagent.ConsistencyReport{}, err
	}
	return manager.Diagnose(ctx)
}

func maintenanceStateDir(requested string) (string, error) {
	_, stateDir, err := resolveDirectories("", strings.TrimSpace(requested))
	if err != nil {
		return "", err
	}
	absolute, err := filepath.Abs(stateDir)
	if err != nil {
		return "", fmt.Errorf("resolve maintenance StateDir: %w", err)
	}
	return filepath.Clean(absolute), nil
}
