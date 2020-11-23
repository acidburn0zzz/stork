package kea

import (
	"context"
	"fmt"
	"time"

	errors "github.com/pkg/errors"
	log "github.com/sirupsen/logrus"
	keaconfig "isc.org/stork/appcfg/kea"
	keactrl "isc.org/stork/appctrl/kea"
	"isc.org/stork/server/agentcomm"
	dbops "isc.org/stork/server/database"
	dbmodel "isc.org/stork/server/database/model"
	"isc.org/stork/server/eventcenter"
	storkutil "isc.org/stork/util"
)

const (
	dhcp4 = "dhcp4"
	dhcp6 = "dhcp6"
	d2    = "d2"
)

// Get list of hooks for all DHCP daemons of the given Kea application.
func GetDaemonHooks(dbApp *dbmodel.App) map[string][]string {
	hooksByDaemon := make(map[string][]string)

	// go through response list with configs from each daemon and retrieve their hooks lists
	for _, dmn := range dbApp.Daemons {
		if dmn.KeaDaemon == nil || dmn.KeaDaemon.Config == nil {
			continue
		}

		libraries := dmn.KeaDaemon.Config.GetHooksLibraries()
		hooks := []string{}
		for _, library := range libraries {
			hooks = append(hooks, library.Library)
		}
		hooksByDaemon[dmn.Name] = hooks
	}

	return hooksByDaemon
}

// === version-get response structs ===============================================

type VersionGetRespArgs struct {
	Extended string
}

type VersionGetResponse struct {
	keactrl.ResponseHeader
	Arguments *VersionGetRespArgs `json:"arguments,omitempty"`
}

// Convenience function called from getStateFromCA and getStateFromDaemons which searches
// for the existing daemon within the app. If the daemon does not exist a new instance is
// created. Otherwise, the function ensures that the KeaDaemon structure is initialized
// and that the active flag is set to true. It returns a pointer to the copy of the
// daemon.
func findDaemonEnsureKeaDaemonIsNotNilAndSetActiveFlag(dbApp *dbmodel.App, daemonName string) *dbmodel.Daemon {
	for _, dmn := range dbApp.Daemons {
		if dmn.Name == daemonName {
			dmnCopy := *dmn
			// Since this is Kea daemon, make sure that the KeaDaemon structure is
			// initialized to avoid nil pointer dereference.
			if dmnCopy.KeaDaemon == nil {
				dmnCopy.KeaDaemon = &dbmodel.KeaDaemon{}
			}
			// Let's assume the daemon is initially active.
			dmnCopy.Active = true
			return &dmnCopy
		}
	}
	return dbmodel.NewKeaDaemon(daemonName, true)
}

// Get state of Kea application Control Agent using ForwardToKeaOverHTTP function.
// The state, that is stored into dbApp, includes: version and config of CA.
// It also returns:
// - list of all Kea daemons
// - list of DHCP daemons (dhcpv4 and/or dhcpv6).
func getStateFromCA(ctx context.Context, agents agentcomm.ConnectedAgents, dbApp *dbmodel.App, daemonsMap map[string]*dbmodel.Daemon, daemonsErrors map[string]string) (keactrl.Daemons, keactrl.Daemons, error) {
	// prepare the command to get config and version from CA
	cmds := []*keactrl.Command{
		{
			Command: "version-get",
		},
		{
			Command: "config-get",
		},
	}

	// get version and config from CA
	versionGetResp := []VersionGetResponse{}
	caConfigGetResp := []keactrl.Response{}

	cmdsResult, err := agents.ForwardToKeaOverHTTP(ctx, dbApp, cmds, &versionGetResp, &caConfigGetResp)
	if err != nil {
		return nil, nil, err
	}
	if cmdsResult.Error != nil {
		return nil, nil, cmdsResult.Error
	}

	daemonsMap["ca"] = findDaemonEnsureKeaDaemonIsNotNilAndSetActiveFlag(dbApp, "ca")

	// if no error in the version-get response then copy retrieved info about CA to its record
	dmn := daemonsMap["ca"]
	err = cmdsResult.CmdsErrors[0]
	if err != nil || len(versionGetResp) == 0 || versionGetResp[0].Result != 0 {
		dmn.Active = false
		errStr := "problem with version-get response from CA: "
		switch {
		case err != nil:
			errStr += fmt.Sprintf("%s", err)
		case len(versionGetResp) == 0:
			errStr += "empty response"
		default:
			errStr += fmt.Sprintf("result == %d, msg: %s", versionGetResp[0].Result, versionGetResp[0].Text)
		}
		log.Warnf(errStr)
		daemonsErrors["ca"] = errStr
		return nil, nil, err
	}

	dmn.Version = versionGetResp[0].Text
	dbApp.Meta.Version = versionGetResp[0].Text
	if versionGetResp[0].Arguments != nil {
		dmn.ExtendedVersion = versionGetResp[0].Arguments.Extended
	}

	// if no error in the config-get response then copy retrieved info about available daemons
	if len(caConfigGetResp) == 0 || caConfigGetResp[0].Arguments == nil || caConfigGetResp[0].Result != 0 {
		dmn.Active = false
		errStr := "problem with config-get response from CA: "
		if len(caConfigGetResp) == 0 || caConfigGetResp[0].Arguments == nil {
			errStr += "response is empty"
		} else {
			errStr += fmt.Sprintf("result == %d, msg: %s", caConfigGetResp[0].Result, caConfigGetResp[0].Text)
		}
		log.Warnf(errStr)
		daemonsErrors["ca"] = errStr
		return nil, nil, err
	}

	// prepare a set of available daemons
	allDaemons := make(keactrl.Daemons)
	dhcpDaemons := make(keactrl.Daemons)

	// Set the configuration for the daemon and populate selected configuration
	// information to the respective structures, e.g. logging information.
	err = dmn.SetConfig(dbmodel.NewKeaConfig(caConfigGetResp[0].Arguments))
	if err != nil {
		return nil, nil, err
	}

	sockets := dmn.KeaDaemon.Config.GetControlSockets()
	if sockets.Dhcp4 != nil {
		allDaemons[dhcp4] = true
		dhcpDaemons[dhcp4] = true
	}
	if sockets.Dhcp6 != nil {
		allDaemons[dhcp6] = true
		dhcpDaemons[dhcp6] = true
	}
	if sockets.D2 != nil {
		allDaemons[d2] = true
	}

	return allDaemons, dhcpDaemons, nil
}

// Get state of Kea application daemons (beside Control Agent) using ForwardToKeaOverHTTP function.
// The state, that is stored into dbApp, includes: version, config and runtime state of indicated Kea daemons.
func getStateFromDaemons(ctx context.Context, agents agentcomm.ConnectedAgents, dbApp *dbmodel.App, daemonsMap map[string]*dbmodel.Daemon, allDaemons keactrl.Daemons, dhcpDaemons keactrl.Daemons, daemonsErrors map[string]string) error {
	now := storkutil.UTCNow()

	// issue 3 commands to Kea daemons at once to get their state
	cmds := []*keactrl.Command{
		{
			Command: "version-get",
			Daemons: &allDaemons,
		},
		{
			Command: "status-get",
			Daemons: &dhcpDaemons,
		},
		{
			Command: "config-get",
			Daemons: &allDaemons,
		},
	}

	versionGetResp := []VersionGetResponse{}
	statusGetResp := []StatusGetResponse{}
	configGetResp := []keactrl.Response{}

	cmdsResult, err := agents.ForwardToKeaOverHTTP(ctx, dbApp, cmds, &versionGetResp, &statusGetResp, &configGetResp)
	if err != nil {
		return err
	}
	if cmdsResult.Error != nil {
		return cmdsResult.Error
	}

	// first find old records of daemons in old daemons assigned to the app
	for name := range allDaemons {
		daemonsMap[name] = findDaemonEnsureKeaDaemonIsNotNilAndSetActiveFlag(dbApp, name)
	}

	// process version-get responses
	err = cmdsResult.CmdsErrors[0]
	if err != nil {
		return errors.WithMessage(err, "problem with version-get response")
	}

	for _, vRsp := range versionGetResp {
		dmn, ok := daemonsMap[vRsp.Daemon]
		if !ok {
			log.Warnf("unrecognized daemon in version-get response: %v", vRsp)
			continue
		}
		if vRsp.Result != 0 {
			dmn.Active = false
			errStr := fmt.Sprintf("problem with version-get and kea daemon %s: %s", vRsp.Daemon, vRsp.Text)
			log.Warnf(errStr)
			daemonsErrors[dmn.Name] = errStr
			continue
		}

		dmn.Version = vRsp.Text
		if vRsp.Arguments != nil {
			dmn.ExtendedVersion = vRsp.Arguments.Extended
		}
	}

	// process status-get responses
	err = cmdsResult.CmdsErrors[1]
	if err != nil {
		return errors.WithMessage(err, "problem with status-get response")
	}

	for _, sRsp := range statusGetResp {
		dmn, ok := daemonsMap[sRsp.Daemon]
		if !ok {
			log.Warnf("unrecognized daemon in status-get response: %v", sRsp)
			continue
		}
		if sRsp.Result != 0 {
			dmn.Active = false
			errStr := fmt.Sprintf("problem with status-get and kea daemon %s: %s", sRsp.Daemon, sRsp.Text)
			log.Warnf(errStr)
			daemonsErrors[dmn.Name] = errStr
			continue
		}

		if sRsp.Arguments != nil {
			dmn.Uptime = sRsp.Arguments.Uptime
			dmn.ReloadedAt = now.Add(time.Second * time.Duration(-sRsp.Arguments.Reload))
		}
	}

	// process config-get responses
	err = cmdsResult.CmdsErrors[2]
	if err != nil {
		return errors.WithMessage(err, "problem with config-get response")
	}

	for _, cRsp := range configGetResp {
		dmn, ok := daemonsMap[cRsp.Daemon]
		if !ok {
			log.Warnf("unrecognized daemon in config-get response: %v", cRsp)
			continue
		}
		if cRsp.Result != 0 {
			dmn.Active = false
			errStr := fmt.Sprintf("problem with config-get and kea daemon %s: %s", cRsp.Daemon, cRsp.Text)
			log.Warnf(errStr)
			daemonsErrors[dmn.Name] = errStr
			continue
		}

		// Set the configuration for the daemon and populate selected configuration
		// information to the respective structures, e.g. logging information.
		err = dmn.SetConfig(dbmodel.NewKeaConfig(cRsp.Arguments))
		if err != nil {
			errStr := fmt.Sprintf("%s", err)
			log.Warn(errStr)
			daemonsErrors[dmn.Name] = errStr
			continue
		}
	}

	return nil
}

// Get state of Kea application daemons using ForwardToKeaOverHTTP function.
// The state that is stored into dbApp includes: version, config and runtime state of indicated Kea daemons.
func GetAppState(ctx context.Context, agents agentcomm.ConnectedAgents, dbApp *dbmodel.App, eventCenter eventcenter.EventCenter) []*dbmodel.Event {
	ctx2, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()

	// get state from CA
	daemonsMap := map[string]*dbmodel.Daemon{}
	daemonsErrors := map[string]string{}
	allDaemons, dhcpDaemons, err := getStateFromCA(ctx2, agents, dbApp, daemonsMap, daemonsErrors)
	if err != nil {
		log.Warnf("problem with getting state from Kea CA: %s", err)
	}

	// if no problems then now get state from the rest of Kea daemons
	err = getStateFromDaemons(ctx2, agents, dbApp, daemonsMap, allDaemons, dhcpDaemons, daemonsErrors)
	if err != nil {
		log.Warnf("problem with getting state from Kea daemons: %s", err)
	}

	newActive, overrideDaemons, newDaemons, events := findChangesAndRaiseEvents(dbApp, daemonsMap, daemonsErrors)

	// update app state
	dbApp.Active = newActive
	if overrideDaemons {
		dbApp.Daemons = newDaemons
	}

	return events
}

func findChangesAndRaiseEvents(dbApp *dbmodel.App, daemonsMap map[string]*dbmodel.Daemon, daemonsErrors map[string]string) (bool, bool, []*dbmodel.Daemon, []*dbmodel.Event) {
	newActive := true
	var newDaemons []*dbmodel.Daemon
	var events []*dbmodel.Event

	// If app already existed then...
	if dbApp.ID != 0 {
		newCADmn, ok := daemonsMap["ca"]
		if !ok || !newCADmn.Active {
			for _, oldDmn := range dbApp.Daemons {
				if oldDmn.Active {
					// add ref to app in daemon so it is available in CreateEvent
					oldDmn.App = dbApp
					oldDmn.Active = false
					errStr, ok := daemonsErrors[oldDmn.Name]
					if !ok {
						errStr = ""
					}
					ev := eventcenter.CreateEvent(dbmodel.EvError, "{daemon} is unreachable", errStr, dbApp.Machine, dbApp, oldDmn)
					events = append(events, ev)
				}
			}
			if dbApp.Active {
				ev := eventcenter.CreateEvent(dbmodel.EvError, "{app} is unreachable", dbApp.Machine, dbApp)
				events = append(events, ev)
			}
			return false, false, nil, events
		}

		for name := range daemonsMap {
			dmn := daemonsMap[name]
			// if all daemons are active then whole app is active
			newActive = newActive && dmn.Active

			// if this is a new app then just add detected daemon
			newDaemons = append(newDaemons, dmn)

			// Determine changes in app daemons state and store them as events.
			// Later this events will be passed to EventCenter when all the changes
			// are stored in database.
			for _, oldDmn := range dbApp.Daemons {
				if dmn.Name == oldDmn.Name {
					// add ref to app in daemon so it is available in CreateEvent
					oldDmn.App = dbApp
					// check if daemon changed Active state
					if dmn.Active != oldDmn.Active {
						lvl := dbmodel.EvWarning
						text := "{daemon} is "
						if dmn.Active && !oldDmn.Active {
							text += "reachable now"
						} else if !dmn.Active && oldDmn.Active {
							text += "unreachable"
							lvl = dbmodel.EvError
						}
						errStr, ok := daemonsErrors[oldDmn.Name]
						if !ok {
							errStr = ""
						}
						ev := eventcenter.CreateEvent(lvl, text, errStr, dbApp.Machine, dbApp, oldDmn)
						events = append(events, ev)

						// check if daemon has been restarted
					} else if dmn.Uptime < oldDmn.Uptime {
						text := "{daemon} has been restarted"
						ev := eventcenter.CreateEvent(dbmodel.EvWarning, text, dbApp.Machine, dbApp, oldDmn)
						events = append(events, ev)
					}

					// check if daemon changed Version
					if dmn.Version != oldDmn.Version {
						text := fmt.Sprintf("{daemon} version changed from %s to %s",
							oldDmn.Version, dmn.Version)
						ev := eventcenter.CreateEvent(dbmodel.EvWarning, text, dbApp.Machine, dbApp, oldDmn)
						events = append(events, ev)
					}
					break
				}
			}
		}
	} else {
		for name := range daemonsMap {
			dmn := daemonsMap[name]
			// if all daemons are active then whole app is active
			newActive = newActive && dmn.Active

			// if this is new daemon and it is not active then disable its monitoring
			if !dmn.Active {
				dmn.Monitored = false
			}

			// if this is a new app then just add detected daemon
			newDaemons = append(newDaemons, dmn)
		}
	}

	return newActive, true, newDaemons, events
}

// Inserts or updates information about Kea app in the database. Next, it extracts
// Kea's configurations and uses to either update or create new shared networks,
// subnets and pools. Finally, the relations between the subnets and the Kea app
// are created. Note that multiple apps can be associated with the same subnet.
func CommitAppIntoDB(db *dbops.PgDB, app *dbmodel.App, eventCenter eventcenter.EventCenter, changeEvents []*dbmodel.Event) error {
	// Go over the shared networks and subnets stored in the Kea configuration
	// and match them with the existing entires in the database. If some of
	// the shared networks or subnets do not exist they are instantiated and
	// returned here.
	networks, subnets, err := DetectNetworks(db, app)
	if err != nil {
		err = errors.Wrapf(err, "unable to detect subnets and shared networks for Kea app with id %d", app.ID)
		return err
	}

	// Go over the global reservations stored in the Kea configuration and
	// match them with the existing global hosts.
	globalHosts, err := detectGlobalHostsFromConfig(db, app)
	if err != nil {
		err = errors.Wrapf(err, "unable to detect global host reservations for Kea app with id %d", app.ID)
		return err
	}

	// Begin transaction.
	tx, rollback, commit, err := dbops.Transaction(db)
	if err != nil {
		return err
	}
	defer rollback()

	newApp := false
	var addedDaemons, deletedDaemons []*dbmodel.Daemon
	if app.ID == 0 {
		// New app, insert it.
		addedDaemons, err = dbmodel.AddApp(tx, app)
		newApp = true
	} else {
		// Existing app, update it if needed.
		addedDaemons, deletedDaemons, err = dbmodel.UpdateApp(tx, app)
	}

	if err != nil {
		return err
	}

	if newApp {
		eventCenter.AddInfoEvent("added {app} on {machine}", app.Machine, app)
	}

	for _, dmn := range deletedDaemons {
		dmn.App = app
		eventCenter.AddInfoEvent("removed {daemon} from {app}", app.Machine, app, dmn)
	}
	for _, dmn := range addedDaemons {
		dmn.App = app
		eventCenter.AddInfoEvent("added {daemon} to {app}", app.Machine, app, dmn)
	}
	for _, ev := range changeEvents {
		eventCenter.AddEvent(ev)
	}

	// Associating an app with subnets is expensive operation. It involves finding
	// local subnet id for each subnet. In order for this operation to be efficient
	// we have to index subnets in the Kea configurations so the local subnet id
	// can be quickly found by subnet prefix.
	for i := range app.Daemons {
		if app.Daemons[i].KeaDaemon != nil &&
			app.Daemons[i].KeaDaemon.Config != nil &&
			app.Daemons[i].KeaDaemon.KeaDHCPDaemon != nil {
			indexedSubnets := keaconfig.NewIndexedSubnets(app.Daemons[i].KeaDaemon.Config)
			err = indexedSubnets.Populate()
			if err != nil {
				return err
			}
			app.Daemons[i].KeaDaemon.KeaDHCPDaemon.IndexedSubnets = indexedSubnets
		}
	}

	// For the given app, iterate over the networks and subnets and update their
	// global instances accordingly in the database.
	addedSubnets, err := dbmodel.CommitNetworksIntoDB(tx, networks, subnets, app, 1)
	if err != nil {
		return err
	}
	if len(addedSubnets) > 0 {
		// add event per subnet only if there is not more than 10 subnets
		if len(addedSubnets) < 10 {
			for _, sn := range addedSubnets {
				eventCenter.AddInfoEvent("added {subnet} to {app}", app, sn)
			}
		}
		t := fmt.Sprintf("added %d subnets to {app}", len(addedSubnets))
		eventCenter.AddInfoEvent(t, app)
	}

	// For the given app, iterate over the global hosts and update their instances
	// in the database or insert them into the database.
	err = dbmodel.CommitGlobalHostsIntoDB(tx, globalHosts, app, "config", 1)
	if err != nil {
		return err
	}

	for _, daemon := range app.Daemons {
		// Check what HA services the daemon belongs to.
		services := DetectHAServices(db, daemon)

		// For the given daemon, iterate over the services and add/update them in the
		// database.
		err = dbmodel.CommitServicesIntoDB(tx, services, daemon)
		if err != nil {
			return err
		}
	}

	// Commit the changes if everything went fine.
	err = commit()
	return err
}
