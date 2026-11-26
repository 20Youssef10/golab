package cli

import "github.com/spf13/cobra"

// registerCommands is the single registration point. Additional command
// families can be added by appending a small registerXCommands helper here.
func registerCommands(root *cobra.Command) {
	registerSessionCommands(root)
	registerExecutionCommands(root)
	registerRunCommands(root)
	registerResumeCommands(root)
	registerFileCommands(root)
	registerUsageCommands(root)
	registerUtilityCommands(root)
	registerSSHCommands(root)
	registerAutomationCommands(root)
	registerUpdateCommand(root)
	registerExtraUtilityCommands(root)
	RegisterLogoutCommands(root)
	RegisterDoctorCommands(root)
	RegisterAuditCommands(root)
	RegisterBudgetCommands(root)
	RegisterBundleCommands(root)
	RegisterAgentOutCommands(root)
	RegisterSyncCommands(root)
	registerAttachCommand(root)
}
