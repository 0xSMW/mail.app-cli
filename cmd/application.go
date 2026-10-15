package cmd

import (
	"time"

	"github.com/0xSMW/mail.app-cli/v3/internal/config"
	"github.com/0xSMW/mail.app-cli/v3/internal/output"
	"github.com/0xSMW/mail.app-cli/v3/pkg/mail"
	"github.com/spf13/cobra"
)

// application owns a fresh command tree and all state for one invocation.
type application struct {
	options
	flagAccount string
	flagMailbox string
	flagBackend string
	outFlags    output.Flags
	writer      *output.Writer
	// Retain failed loads for fallback formatting without reading config twice.
	cfg                     config.Config
	resolved                config.Resolved
	mailClient              *mail.Client
	configLoadAttempted     bool
	accountsCmd             *cobra.Command
	accountsListCmd         *cobra.Command
	attachmentsCmd          *cobra.Command
	attachmentsListCmd      *cobra.Command
	attachmentsSaveCmd      *cobra.Command
	messagesBatchCmd        *cobra.Command
	messagesBatchArchiveCmd *cobra.Command
	messagesBatchDeleteCmd  *cobra.Command
	messagesBatchMoveCmd    *cobra.Command
	messagesBatchMarkCmd    *cobra.Command
	messagesBatchFlagCmd    *cobra.Command
	commandsCmd             *cobra.Command
	configCmd               *cobra.Command
	configSetCmd            *cobra.Command
	configUnsetCmd          *cobra.Command
	configPathCmd           *cobra.Command
	doctorCmd               *cobra.Command
	draftsCmd               *cobra.Command
	draftsListCmd           *cobra.Command
	draftsCreateCmd         *cobra.Command
	draftsShowCmd           *cobra.Command
	draftsUpdateCmd         *cobra.Command
	draftsSendCmd           *cobra.Command
	draftsDeleteCmd         *cobra.Command
	exportCmd               *cobra.Command
	exportMessagesCmd       *cobra.Command
	exportAttachmentsCmd    *cobra.Command
	importCmd               *cobra.Command
	importMessagesCmd       *cobra.Command
	mailboxesCmd            *cobra.Command
	mailboxesListCmd        *cobra.Command
	messagesCmd             *cobra.Command
	messagesListCmd         *cobra.Command
	messagesMarkCmd         *cobra.Command
	messagesFlagCmd         *cobra.Command
	messagesDeleteCmd       *cobra.Command
	messagesArchiveCmd      *cobra.Command
	messagesMoveCmd         *cobra.Command
	messagesInboxCmd        *cobra.Command
	messagesUnreadCmd       *cobra.Command
	messagesSentCmd         *cobra.Command
	messagesDraftsCmd       *cobra.Command
	messagesFlaggedCmd      *cobra.Command
	messagesTrashCmd        *cobra.Command
	messagesJunkCmd         *cobra.Command
	recentCmd               *cobra.Command
	recentSearchCmd         *cobra.Command
	recentClearCmd          *cobra.Command
	rootCmd                 *cobra.Command
	rulesCmd                *cobra.Command
	rulesListCmd            *cobra.Command
	rulesEnableCmd          *cobra.Command
	rulesDisableCmd         *cobra.Command
	rulesDeleteCmd          *cobra.Command
	rulesCreateCmd          *cobra.Command
	rulesApplyCmd           *cobra.Command
	searchCmd               *cobra.Command
	sendCmd                 *cobra.Command
	signaturesCmd           *cobra.Command
	signaturesListCmd       *cobra.Command
	skillCmd                *cobra.Command
	skillInstallCmd         *cobra.Command
	smartCmd                *cobra.Command
	smartListCmd            *cobra.Command
	smartQueryCmd           *cobra.Command
	syncCmd                 *cobra.Command
	threadsCmd              *cobra.Command
	threadsListCmd          *cobra.Command
	threadsArchiveCmd       *cobra.Command
	tuiCmd                  *cobra.Command
	inboxCmd                *cobra.Command
	unreadCmd               *cobra.Command
	showCmd                 *cobra.Command
	seenCmd                 *cobra.Command
	unseenCmd               *cobra.Command
	flagCmd                 *cobra.Command
	unflagCmd               *cobra.Command
	archiveCmd              *cobra.Command
	deleteCmd               *cobra.Command
	moveCmd                 *cobra.Command
	versionCmd              *cobra.Command
	messagesVIPCmd          *cobra.Command
}

// options contains the values Cobra binds while parsing this invocation.
type options struct {
	labelsEmail         string
	batchQuery          string
	batchSender         string
	batchSenderDomain   string
	batchStdin          bool
	batchDryRun         bool
	batchYes            bool
	batchRead           bool
	batchFlagged        bool
	batchMarkReadBefore bool
	batchMarkReadOnSkip bool
	batchSettle         time.Duration
	batchVerifyBudget   time.Duration
	batchVerify         bool
	batchProgress       bool
	batchChunkSize      int
	batchLimit          int
	batchReportFile     string
	exportOutput        string
	msgLimit            int
	msgOffset           int
	msgWithContent      bool
	ruleDryRun          bool
	threadLimit         int
	verbDryRun          bool
	verbVerify          bool
	verbSettle          time.Duration
	verbVerifyBudget    time.Duration
}

func newApplication() *application {
	a := &application{}
	a.buildAccountsCommands()
	a.buildAttachmentsCommands()
	a.buildBatchCommands()
	a.buildInventoryCommand()
	a.buildConfigCommands()
	a.buildDoctorCommands()
	a.buildDraftsCommands()
	a.buildExportCommands()
	a.buildImportCommands()
	a.buildMailboxesCommands()
	a.buildVIPCommand()
	a.buildMessagesCommands()
	a.buildRecentCommands()
	a.buildRulesCommands()
	a.buildSearchCommands()
	a.buildSendCommands()
	a.buildSignaturesCommands()
	a.buildSkillCommands()
	a.buildSmartCommands()
	a.buildSyncCommands()
	a.buildThreadsCommands()
	a.buildTUICommand()
	a.buildVerbsCommands()
	a.buildVersionCommands()
	a.messagesCmd.AddCommand(a.newMessagesReadCmd(), a.newMessagesReconcileCmd(), a.newMessagesScanCmd())
	a.buildRootCommands()
	a.rootCmd.AddCommand(a.newLabelsCmd())
	a.markMutations()
	return a
}
