using Datadog.AgentCustomActions;
using System.Diagnostics.CodeAnalysis;
using WixSharp;

namespace WixSetup.Datadog_Agent
{
    public class AgentCustomActions
    {
        [SuppressMessage("ReSharper", "InconsistentNaming")]
        private static readonly Condition Being_Reinstalled = Condition.Create("(REINSTALL<>\"\")");

        [SuppressMessage("ReSharper", "InconsistentNaming")]
        private static readonly Condition NOT_Being_Reinstalled = Condition.NOT(Being_Reinstalled);

        public ManagedAction RunAsAdmin { get; }

        public ManagedAction ReadConfig { get; }

        public ManagedAction PatchInstaller { get; set; }

        public ManagedAction SetupInstaller { get; set; }

        public ManagedAction EnsureGeneratedFilesRemoved { get; }

        public ManagedAction WriteConfig { get; }

        public ManagedAction ReadInstallState { get; }

        public ManagedAction WriteInstallState { get; }

        public ManagedAction DeleteInstallState { get; }

        public ManagedAction ProcessDdAgentUserCredentials { get; }

        public ManagedAction ProcessDdAgentUserCredentialsUI { get; }

        public ManagedAction PrepareDecompressPythonDistributions { get; }

        public ManagedAction RunPostInstPythonScript { get; }

        public ManagedAction RunPreRemovePythonScriptRollback { get; }

        public ManagedAction RunPreRemovePythonScript { get; }

        public ManagedAction DecompressPythonDistributions { get; }

        public ManagedAction CleanupOnRollback { get; }

        public ManagedAction CleanupOnUninstall { get; }

        public ManagedAction ConfigureUser { get; }

        public ManagedAction ConfigureUserRollback { get; }

        public ManagedAction UninstallUser { get; }

        public ManagedAction UninstallUserRollback { get; }

        public ManagedAction OpenMsiLog { get; }

        public ManagedAction SendFlare { get; }

        public ManagedAction InstallOciPackages { get; }

        public ManagedAction RollbackOciPackages { get; }

        public ManagedAction WriteInstallInfo { get; }

        public ManagedAction ReportInstallFailure { get; }

        public ManagedAction ReportInstallSuccess { get; }

        public ManagedAction EnsureNpmServiceDepdendency { get; }

        public ManagedAction ConfigureServices { get; }

        public ManagedAction ConfigureServicesRollback { get; }

        public ManagedAction StopDDServices { get; }

        public ManagedAction StartDDServices { get; }

        public ManagedAction StartDDServicesRollback { get; }

        public ManagedAction RestoreDaclRollback { get; }

        public ManagedAction DDCreateFolders { get; }

        /// <summary>
        /// Registers and sequences our custom actions
        /// </summary>
        /// <remarks>
        /// Please refer to https://learn.microsoft.com/en-us/windows/win32/msi/sequencing-custom-actions
        /// </remarks>
        public AgentCustomActions()
        {
            RunAsAdmin = new CustomAction<CustomActions>(
                new Id(nameof(RunAsAdmin)),
                CustomActions.EnsureAdminCaller,
                Return.check,
                When.After,
                Step.AppSearch,
                Condition.Always,
                Sequence.InstallExecuteSequence | Sequence.InstallUISequence);

            ReadInstallState = new CustomAction<CustomActions>(
                new Id(nameof(ReadInstallState)),
                CustomActions.ReadInstallState,
                Return.check,
                // AppSearch is when ReadInstallState is run, so that will overwrite
                // any command line values.
                // Prefer using our CA over RegistrySearch.
                // It is executed on the Welcome screen of the installer.
                // Must run before CostInitialize and WixRemoveFoldersEx since it creates properties used by util:RemoveFolderEx
                When.After,
                new Step(RunAsAdmin.Id),
                // Creates properties used by both install+uninstall
                Condition.Always,
                // Run in either sequence so our CA is also run in non-UI installs
                Sequence.InstallExecuteSequence | Sequence.InstallUISequence
            )
            {
                // Ensure we only run in one sequence
                Execute = Execute.firstSequence
            };

            // We need to explicitly set the ID since that we are going to reference before the Build* call.
            // See <see cref="WixSharp.WixEntity.Id" /> for more information.
            ReadConfig = new CustomAction<CustomActions>(
                    new Id(nameof(ReadConfig)),
                    CustomActions.ReadConfig,
                    Return.ignore,
                    When.After,
                    // Must execute after CostFinalize since we depend
                    // on APPLICATIONDATADIRECTORY being set.
                    Step.CostFinalize,
                    // Not needed during uninstall, but since it runs before InstallValidate the recommended
                    // REMOVE=ALL condition does not work, so always run it.
                    Condition.Always,
                    // Run in either sequence so our CA is also run in non-UI installs
                    Sequence.InstallExecuteSequence | Sequence.InstallUISequence
                )
            {
                // Ensure we only run in one sequence
                Execute = Execute.firstSequence
            }
                .SetProperties("APPLICATIONDATADIRECTORY=[APPLICATIONDATADIRECTORY]");

            PatchInstaller = new CustomAction<CustomActions>(
                new Id(nameof(PatchInstaller)),
                CustomActions.Patch,
                Return.ignore,
                When.After,
                Step.InstallFiles,
                Conditions.Upgrading
            )
            {
                Execute = Execute.deferred,
                Impersonate = false
            };

            ReportInstallFailure = new CustomAction<CustomActions>(
                    new Id(nameof(ReportInstallFailure)),
                    CustomActions.ReportFailure,
                    Return.ignore,
                    When.After,
                    Step.InstallInitialize
                )
            {
                Execute = Execute.rollback,
                Impersonate = false
            }
                .SetProperties("APIKEY=[APIKEY], SITE=[SITE]")
                .HideTarget(true);

            EnsureNpmServiceDepdendency = new CustomAction<CustomActions>(
                new Id(nameof(EnsureNpmServiceDepdendency)),
                CustomActions.EnsureNpmServiceDependency,
                Return.check,
                When.After,
                Step.InstallServices,
                Conditions.FirstInstall | Conditions.Upgrading | Conditions.Maintenance
            )
            {
                Execute = Execute.deferred,
                Impersonate = false
            };

            EnsureGeneratedFilesRemoved = new CustomAction<CustomActions>(
                new Id(nameof(EnsureGeneratedFilesRemoved)),
                CustomActions.CleanupFiles,
                Return.check,
                When.Before,
                Step.InstallFiles,
                Conditions.FirstInstall | Conditions.Upgrading | Conditions.Maintenance
            )
            {
                Execute = Execute.deferred,
                Impersonate = false
            }
                .SetProperties(
                    "PROJECTLOCATION=[PROJECTLOCATION], APPLICATIONDATADIRECTORY=[APPLICATIONDATADIRECTORY]");

            WriteConfig = new CustomAction<CustomActions>(
                    new Id(nameof(WriteConfig)),
                    CustomActions.WriteConfig,
                    Return.check,
                    When.Before,
                    Step.InstallServices,
                    Conditions.FirstInstall | Conditions.Upgrading | Conditions.Maintenance
                )
            {
                Execute = Execute.deferred,
                Impersonate = false
            }
                .SetProperties(
                    "APPLICATIONDATADIRECTORY=[APPLICATIONDATADIRECTORY], " +
                    "PROJECTLOCATION=[PROJECTLOCATION], " +
                    "SYSPROBE_PRESENT=[SYSPROBE_PRESENT], " +
                    "APIKEY=[APIKEY], " +
                    "TAGS=[TAGS], " +
                    "HOSTNAME=[HOSTNAME], " +
                    "PROXY_HOST=[PROXY_HOST], " +
                    "PROXY_PORT=[PROXY_PORT], " +
                    "PROXY_USER=[PROXY_USER], " +
                    "PROXY_PASSWORD=[PROXY_PASSWORD], " +
                    "LOGS_ENABLED=[LOGS_ENABLED], " +
                    "APM_ENABLED=[APM_ENABLED], " +
                    "PROCESS_ENABLED=[PROCESS_ENABLED], " +
                    "PROCESS_DISCOVERY_ENABLED=[PROCESS_DISCOVERY_ENABLED], " +
                    "CMD_PORT=[CMD_PORT], " +
                    "SITE=[SITE], " +
                    "DD_URL=[DD_URL], " +
                    "LOGS_DD_URL=[LOGS_DD_URL], " +
                    "PROCESS_DD_URL=[PROCESS_DD_URL], " +
                    "TRACE_DD_URL=[TRACE_DD_URL], " +
                    "PYVER=[PYVER], " +
                    "HOSTNAME_FQDN_ENABLED=[HOSTNAME_FQDN_ENABLED], " +
                    "NPM=[NPM], " +
                    "EC2_USE_WINDOWS_PREFIX_DETECTION=[EC2_USE_WINDOWS_PREFIX_DETECTION]")
                .HideTarget(true);

            // Cleanup leftover files on rollback
            // must be before the DecompressPythonDistributions custom action.
            // That way, if DecompressPythonDistributions fails, this will get executed.
            CleanupOnRollback = new CustomAction<CustomActions>(
                    new Id(nameof(CleanupOnRollback)),
                    CustomActions.CleanupFiles,
                    Return.check,
                    When.After,
                    new Step(WriteConfig.Id),
                    Conditions.FirstInstall | Conditions.Upgrading | Conditions.Maintenance
                )
            {
                Execute = Execute.rollback,
                Impersonate = false
            }
                .SetProperties(
                    "PROJECTLOCATION=[PROJECTLOCATION], APPLICATIONDATADIRECTORY=[APPLICATIONDATADIRECTORY]");

            DecompressPythonDistributions = new CustomAction<CustomActions>(
                    new Id(nameof(DecompressPythonDistributions)),
                    CustomActions.DecompressPythonDistributions,
                    Return.check,
                    When.After,
                    new Step(CleanupOnRollback.Id),
                    Conditions.FirstInstall | Conditions.Upgrading | Conditions.Maintenance
                )
            {
                Execute = Execute.deferred,
                Impersonate = false
            }
                .SetProperties(
                    "PROJECTLOCATION=[PROJECTLOCATION], " +
                    "embedded3_SIZE=[embedded3_SIZE], " +
                    "AgentFlavor=[AgentFlavor]");

            PrepareDecompressPythonDistributions = new CustomAction<CustomActions>(
                new Id(nameof(PrepareDecompressPythonDistributions)),
                CustomActions.PrepareDecompressPythonDistributions,
                Return.ignore,
                When.Before,
                new Step(DecompressPythonDistributions.Id),
                Conditions.FirstInstall | Conditions.Upgrading | Conditions.Maintenance,
                Sequence.InstallExecuteSequence
            )
            {
                Execute = Execute.immediate
            };

            RunPostInstPythonScript = new CustomAction<CustomActions>(
                    new Id(nameof(RunPostInstPythonScript)),
                    CustomActions.RunPostInstPythonScript,
                    // we now ignore this custom action result to assure there are no failures resulting from
                    // issues installing third party integrations
                    Return.ignore,
                    When.After,
                    Step.InstallServices,
                    Conditions.FirstInstall | Conditions.Upgrading | Conditions.Maintenance
                )
            {
                Execute = Execute.deferred,
                Impersonate = false
            }
                .SetProperties(
                    "PROJECTLOCATION=[PROJECTLOCATION], APPLICATIONDATADIRECTORY=[APPLICATIONDATADIRECTORY], INSTALL_PYTHON_THIRD_PARTY_DEPS=[INSTALL_PYTHON_THIRD_PARTY_DEPS]");

            SetupInstaller = new CustomAction<CustomActions>(
                    new Id(nameof(SetupInstaller)),
                    CustomActions.SetupInstaller,
                    Return.check,
                    When.After,
                    Step.InstallServices,
                    Conditions.FirstInstall | Conditions.Upgrading
                )
            {
                Execute = Execute.deferred,
                Impersonate = false
            }
                .SetProperties(
                    "PROJECTLOCATION=[PROJECTLOCATION], FLEET_INSTALL=[FLEET_INSTALL], DATABASE=[DATABASE]");

            // Cleanup leftover files on uninstall
            CleanupOnUninstall = new CustomAction<CustomActions>(
                    new Id(nameof(CleanupOnUninstall)),
                    CustomActions.CleanupFiles,
                    Return.check,
                    When.Before,
                    Step.RemoveFiles,
                    Conditions.Uninstalling
                )
            {
                Execute = Execute.deferred,
                Impersonate = false
            }
                .SetProperties(
                    "PROJECTLOCATION=[PROJECTLOCATION], APPLICATIONDATADIRECTORY=[APPLICATIONDATADIRECTORY]");

            RunPreRemovePythonScript = new CustomAction<CustomActions>(
                    new Id(nameof(RunPreRemovePythonScript)),
                    CustomActions.RunPreRemovePythonScript,
                    Return.ignore,
                    When.Before,
                    new Step(CleanupOnUninstall.Id),
                    Conditions.RemovingForUpgrade | Conditions.Maintenance | Conditions.Uninstalling
                )
            {
                Execute = Execute.deferred,
                Impersonate = false
            }
                .SetProperties(
                    "PROJECTLOCATION=[PROJECTLOCATION], APPLICATIONDATADIRECTORY=[APPLICATIONDATADIRECTORY]");

            RunPreRemovePythonScriptRollback = new CustomAction<CustomActions>(
                    new Id(nameof(RunPreRemovePythonScriptRollback)),
                    CustomActions.RunPreRemovePythonScriptRollback,
                    Return.check,
                    When.Before,
                    new Step(RunPreRemovePythonScript.Id),
                    Conditions.FirstInstall | Conditions.Upgrading | Conditions.Maintenance
                )
            {
                Execute = Execute.rollback,
                Impersonate = false
            };

            ConfigureUser = new CustomAction<CustomActions>(
                    new Id(nameof(ConfigureUser)),
                    CustomActions.ConfigureUser,
                    Return.check,
                    When.After,
                    new Step(DecompressPythonDistributions.Id),
                    Condition.NOT(Conditions.Uninstalling | Conditions.RemovingForUpgrade)
                )
            {
                Execute = Execute.deferred,
                Impersonate = false
            }
                .SetProperties("APPLICATIONDATADIRECTORY=[APPLICATIONDATADIRECTORY], " +
                               "PROJECTLOCATION=[PROJECTLOCATION], " +
                               "DDAGENTUSER_PROCESSED_NAME=[DDAGENTUSER_PROCESSED_NAME], " +
                               "DDAGENTUSER_PROCESSED_FQ_NAME=[DDAGENTUSER_PROCESSED_FQ_NAME], " +
                               "DDAGENTUSER_PROCESSED_PASSWORD=[DDAGENTUSER_PROCESSED_PASSWORD], " +
                               "DDAGENTUSER_FOUND=[DDAGENTUSER_FOUND], " +
                               "DDAGENTUSER_SID=[DDAGENTUSER_SID], " +
                               "DDAGENTUSER_RESET_PASSWORD=[DDAGENTUSER_RESET_PASSWORD], " +
                               "WIX_UPGRADE_DETECTED=[WIX_UPGRADE_DETECTED], " +
                               "DDAGENTUSER_IS_SERVICE_ACCOUNT=[DDAGENTUSER_IS_SERVICE_ACCOUNT]")
                .HideTarget(true);

            ConfigureUserRollback = new CustomAction<CustomActions>(
                    new Id(nameof(ConfigureUserRollback)),
                    CustomActions.ConfigureUserRollback,
                    Return.check,
                    When.Before,
                    new Step(ConfigureUser.Id),
                    Condition.NOT(Conditions.Uninstalling | Conditions.RemovingForUpgrade)
                )
            {
                Execute = Execute.rollback,
                Impersonate = false,
            };

            UninstallUser = new CustomAction<CustomActions>(
                    new Id(nameof(UninstallUser)),
                    CustomActions.UninstallUser,
                    Return.check,
                    When.After,
                    Step.StopServices,
                    Conditions.Uninstalling | Conditions.RemovingForUpgrade
                )
            {
                Execute = Execute.deferred,
                Impersonate = false
            }
                .SetProperties("APPLICATIONDATADIRECTORY=[APPLICATIONDATADIRECTORY], " +
                               "PROJECTLOCATION=[PROJECTLOCATION], " +
                               "DDAGENTUSER_NAME=[DDAGENTUSER_NAME], " +
                               "UPGRADINGPRODUCTCODE=[UPGRADINGPRODUCTCODE], " +
                               "FLEET_INSTALL=[FLEET_INSTALL]");

            UninstallUserRollback = new CustomAction<CustomActions>(
                    new Id(nameof(UninstallUserRollback)),
                    CustomActions.UninstallUserRollback,
                    Return.check,
                    When.Before,
                    new Step(UninstallUser.Id),
                    Conditions.Uninstalling | Conditions.RemovingForUpgrade
                )
            {
                Execute = Execute.rollback,
                Impersonate = false,
            };

            ProcessDdAgentUserCredentials = new CustomAction<CustomActions>(
                    new Id(nameof(ProcessDdAgentUserCredentials)),
                    CustomActions.ProcessDdAgentUserCredentials,
                    Return.check,
                    // Run at end of "config phase", right before the "make changes" phase.
                    // Ensure no actions that modify the input properties are run after this action.
                    When.Before,
                    Step.InstallInitialize,
                    // Run unless we are being uninstalled.
                    // This CA produces properties used for services, accounts, and permissions.
                    Condition.NOT(Conditions.Uninstalling | Conditions.RemovingForUpgrade)
                )
                .SetProperties("DDAGENTUSER_NAME=[DDAGENTUSER_NAME], " +
                               "DDAGENTUSER_PASSWORD=[DDAGENTUSER_PASSWORD], " +
                               "DDAGENTUSER_PROCESSED_FQ_NAME=[DDAGENTUSER_PROCESSED_FQ_NAME]")
                .HideTarget(true);

            ProcessDdAgentUserCredentialsUI = new CustomAction<CustomActions>(
                new Id(nameof(ProcessDdAgentUserCredentialsUI)),
                CustomActions.ProcessDdAgentUserCredentialsUI
            )
            {
                // Not run in a sequence, run when Next is clicked on ddagentuserdlg
                Sequence = Sequence.NotInSequence
            };

            OpenMsiLog = new CustomAction<CustomActions>(
                new Id(nameof(OpenMsiLog)),
                CustomActions.OpenMsiLog
            )
            {
                // Not run in a sequence, run from button on fatalError dialog
                Sequence = Sequence.NotInSequence
            };

            SendFlare = new CustomAction<CustomActions>(
                new Id(nameof(SendFlare)),
                CustomActions.SendFlare
            )
            {
                // Not run in a sequence, run from button on fatalError dialog
                Sequence = Sequence.NotInSequence
            };

            InstallOciPackages = new CustomAction<CustomActions>(
                    new Id(nameof(InstallOciPackages)),
                    CustomActions.InstallOciPackages,
                    Return.check,
                    When.Before,
                    Step.StartServices,
                    Condition.NOT(Conditions.Uninstalling | Conditions.RemovingForUpgrade)
            )
            {
                Execute = Execute.deferred,
                Impersonate = false
            }
            .SetProperties("PROJECTLOCATION=[PROJECTLOCATION]," +
                           "APIKEY=[APIKEY]," +
                           "SITE=[SITE]," +
                           "DD_INSTALLER_REGISTRY_URL=[DD_INSTALLER_REGISTRY_URL]," +
                           "DD_APM_INSTRUMENTATION_ENABLED=[DD_APM_INSTRUMENTATION_ENABLED]," +
                           "DD_APM_INSTRUMENTATION_LIBRARIES=[DD_APM_INSTRUMENTATION_LIBRARIES]");

            RollbackOciPackages = new CustomAction<CustomActions>(
                    new Id(nameof(RollbackOciPackages)),
                    CustomActions.RollbackOciPackages,
                    Return.ignore,
                    When.Before,
                    new Step(InstallOciPackages.Id),
                    Condition.NOT(Conditions.Uninstalling | Conditions.RemovingForUpgrade)
                )
            {
                Execute = Execute.rollback,
                Impersonate = false
            }
                .SetProperties("PROJECTLOCATION=[PROJECTLOCATION],SITE=[SITE],APIKEY=[APIKEY]");

            WriteInstallInfo = new CustomAction<CustomActions>(
                    new Id(nameof(WriteInstallInfo)),
                    CustomActions.WriteInstallInfo,
                    Return.ignore,
                    When.Before,
                    Step.StartServices,
                    // Include "Being_Reinstalled" so that if customer changes install method
                    // the install_info reflects that.
                    Conditions.FirstInstall | Conditions.Upgrading
                )
            {
                Execute = Execute.deferred,
                Impersonate = false
            }
                .SetProperties("APPLICATIONDATADIRECTORY=[APPLICATIONDATADIRECTORY]," +
                               "OVERRIDE_INSTALLATION_METHOD=[OVERRIDE_INSTALLATION_METHOD]," +
                               "SKIP_INSTALL_INFO=[SKIP_INSTALL_INFO]");

            // Hitting this CustomAction always means the install succeeded
            // because when an install fails, it rollbacks from the `InstallFinalize`
            // step.
            ReportInstallSuccess = new CustomAction<CustomActions>(
                    new Id(nameof(ReportInstallSuccess)),
                    CustomActions.ReportSuccess,
                    Return.ignore,
                    When.After,
                    Step.InstallFinalize,
                    Conditions.FirstInstall | Conditions.Upgrading
                )
                .SetProperties("APIKEY=[APIKEY], SITE=[SITE]")
                .HideTarget(true);

            // Enables the user to change the service accounts during upgrade/change
            // Relies on StopDDServices/StartDDServices to ensure the services are restarted
            // so that the new configuration is used.
            ConfigureServices = new CustomAction<CustomActions>(
                    new Id(nameof(ConfigureServices)),
                    CustomActions.ConfigureServices,
                    Return.check,
                    When.After,
                    Step.InstallServices,
                    Condition.NOT(Conditions.Uninstalling | Conditions.RemovingForUpgrade)
                )
            {
                Execute = Execute.deferred,
                Impersonate = false
            }
                .SetProperties("DDAGENTUSER_PROCESSED_PASSWORD=[DDAGENTUSER_PROCESSED_PASSWORD], " +
                               "DDAGENTUSER_PROCESSED_FQ_NAME=[DDAGENTUSER_PROCESSED_FQ_NAME], ")
                .HideTarget(true);

            ConfigureServicesRollback = new CustomAction<CustomActions>(
                    new Id(nameof(ConfigureServicesRollback)),
                    CustomActions.ConfigureServicesRollback,
                    Return.check,
                    When.Before,
                    new Step(ConfigureServices.Id),
                    Condition.NOT(Conditions.Uninstalling | Conditions.RemovingForUpgrade)
                )
            {
                Execute = Execute.rollback,
                Impersonate = false
            }
                .SetProperties("DDAGENTUSER_PROCESSED_FQ_NAME=[DDAGENTUSER_PROCESSED_FQ_NAME], ")
                .HideTarget(true);

            // WiX built-in StopServices only stops services if the component is changing.
            // This means that the services associated with MainApplication won't be restarted
            // during change operations.
            StopDDServices = new CustomAction<CustomActions>(
                new Id(nameof(StopDDServices)),
                CustomActions.StopDDServices,
                Return.check,
                When.Before,
                Step.StopServices
            )
            {
                Execute = Execute.deferred,
                Impersonate = false
            };

            // WiX built-in StartServices only starts services if the component is changing.
            // This means that the services associated with MainApplication won't be restarted
            // during change operations.
            StartDDServices = new CustomAction<CustomActions>(
                new Id(nameof(StartDDServices)),
                CustomActions.StartDDServices,
                Return.check,
                When.After,
                Step.StartServices,
                Condition.NOT(Conditions.Uninstalling | Conditions.RemovingForUpgrade)
            )
            {
                Execute = Execute.deferred,
                Impersonate = false
            };

            // Rollback StartDDServices stops the the services so that any file locks are released.
            StartDDServicesRollback = new CustomAction<CustomActions>(
                new Id(nameof(StartDDServicesRollback)),
                CustomActions.StartDDServicesRollback,
                Return.ignore,
                // Must be sequenced before the action it will rollback for
                When.Before,
                new Step(StartDDServices.Id),
                // Must have same condition as the action it will rollback for
                Condition.NOT(Conditions.Uninstalling | Conditions.RemovingForUpgrade)
            )
            {
                Execute = Execute.rollback,
                Impersonate = false
            };

            WriteInstallState = new CustomAction<CustomActions>(
                    new Id(nameof(WriteInstallState)),
                    CustomActions.WriteInstallState,
                    Return.check,
                    When.Before,
                    Step.StartServices,
                    // Run unless we are being uninstalled.
                    Condition.NOT(Conditions.Uninstalling | Conditions.RemovingForUpgrade)
                )
            {
                Execute = Execute.deferred,
                Impersonate = false
            }
                .SetProperties("DDAGENTUSER_PROCESSED_DOMAIN=[DDAGENTUSER_PROCESSED_DOMAIN], " +
                               "DDAGENTUSER_PROCESSED_NAME=[DDAGENTUSER_PROCESSED_NAME]");

            DeleteInstallState = new CustomAction<CustomActions>(
                    new Id(nameof(DeleteInstallState)),
                    CustomActions.DeleteInstallState,
                    Return.check,
                    // Since this CA removes registry values it must run before the built-in RemoveRegistryValues
                    // so that the built-in registry keys can be removed if they are empty.
                    When.Before,
                    Step.RemoveRegistryValues,
                    // Run only on full uninstall
                    Conditions.Uninstalling
                )
            {
                Execute = Execute.deferred,
                Impersonate = false
            };

            // This custom action resets the SE_DACL_AUTOINHERITED flag on %PROJECTLOCATION% on uninstall
            // to make sure the uninstall doesn't fail due to the non-canonical permission issue.
            RestoreDaclRollback = new CustomAction<CustomActions>(
                    new Id(nameof(RestoreDaclRollback)),
                    CustomActions.DoRollback,
                    Return.ignore,
                    When.After,
                    // This is the earliest we can schedule this action
                    // during an uninstall
                    Step.InstallInitialize,
                    // Run when REMOVE="ALL" which runs also on upgrade
                    // This ensures this product can be removed before
                    // the new one is installed.
                    Condition.BeingUninstalled)
            {
                Execute = Execute.deferred,
                Impersonate = false
            }.SetProperties("PROJECTLOCATION=[PROJECTLOCATION]");

            DDCreateFolders = new CustomAction<CustomActions>(
                    new Id(nameof(DDCreateFolders)),
                    CustomActions.DDCreateFolders,
                    Return.check,
                    When.Before,
                    Step.CreateFolders,
                    // Run only on FirstInstall.
                    // In Upgrade/Repair the directory has already been
                    // created and configured, and this action could leave the directory
                    // without access for ddagentuser if the installer rolls back.
                    Conditions.FirstInstall
                    )
            {
                Execute = Execute.deferred,
                Impersonate = false
            }.SetProperties("APPLICATIONDATADIRECTORY=[APPLICATIONDATADIRECTORY]");
        }
    }
}
