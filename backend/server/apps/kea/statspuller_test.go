package kea

import (
	"encoding/json"
	"fmt"
	"math"
	"math/big"
	"testing"

	"github.com/go-pg/pg/v10"
	"github.com/stretchr/testify/require"
	keactrl "isc.org/stork/appctrl/kea"
	agentcommtest "isc.org/stork/server/agentcomm/test"
	dbmodel "isc.org/stork/server/database/model"
	dbtest "isc.org/stork/server/database/test"
)

// Prepares the Kea mock. It accepts list of serialized JSON responses in order:
// 1. DHCPv4
// 2. DHCPv4 RSP
// 3. DHCPv6
// 4. DHCPv6 RSP.
func createKeaMock(jsonFactory func(callNo int) (jsons []string)) func(callNo int, cmdResponses []interface{}) {
	return func(callNo int, cmdResponses []interface{}) {
		jsons := jsonFactory(callNo)
		// DHCPv4
		daemons := []string{"dhcp4"}
		command := keactrl.NewCommand("stat-lease4-get", daemons, nil)
		keactrl.UnmarshalResponseList(command, []byte(jsons[0]), cmdResponses[0])

		// DHCPv4 RSP response
		rpsCmd := []*keactrl.Command{}
		_ = RpsAddCmd4(&rpsCmd, daemons)
		keactrl.UnmarshalResponseList(rpsCmd[0], []byte(jsons[1]), cmdResponses[1])

		if len(cmdResponses) < 4 {
			return
		}

		// DHCPv6
		daemons = []string{"dhcp6"}
		command = keactrl.NewCommand("stat-lease6-get", daemons, nil)
		keactrl.UnmarshalResponseList(command, []byte(jsons[2]), cmdResponses[2])

		// DHCPv6 RSP response
		rpsCmd = []*keactrl.Command{}
		_ = RpsAddCmd6(&rpsCmd, daemons)
		keactrl.UnmarshalResponseList(rpsCmd[0], []byte(jsons[3]), cmdResponses[3])
	}
}

func createDhcpConfigs() (string, string) {
	dhcp4 := `{
		"Dhcp4": {
			"hooks-libraries": [
				{
					"library": "/usr/lib/kea/libdhcp_stat_cmds.so"
				}
			],
			"reservations": [
				{
					"hw-address": "01:bb:cc:dd:ee:ff",
					"ip-address": "192.12.0.1"
				},
				{
					"hw-address": "02:bb:cc:dd:ee:ff",
					"ip-address": "192.12.0.2"
				}
			],
			"subnet4": [
				{
					"id": 10,
					"subnet": "192.0.2.0/24"
				},
				{
					"id": 20,
					"subnet": "192.0.3.0/24",
					// 1 in-pool, 2 out-of-pool host reservations
					"pools": [
						{
							"pool": "192.0.3.1 - 192.0.3.10"
						}
					],
					"reservations": [
						{
							"hw-address": "00:00:00:00:00:21",
							"ip-address": "192.0.3.2"
						},
						{
							"hw-address": "00:00:00:00:00:22",
							"ip-address": "192.0.2.22"
						},
						{
							"hw-address": "00:00:00:00:00:23",
							"ip-address": "192.0.2.23"
						}
					]
				}
			]
		}
	}`
	dhcp6 := `{
		"Dhcp6": {
			"hooks-libraries": [
				{
					"library": "/usr/lib/kea/libdhcp_stat_cmds.so"
				}
			],
			"reservations": [
				{
					"hw-address": "03:bb:cc:dd:ee:ff",
					"ip-address": "80:80::1"
				},
				{
					"hw-address": "04:bb:cc:dd:ee:ff",
					"ip-address": "80:90::/64"
				}
			],
			"subnet6": [
				{
					"id": 30,
					"subnet": "2001:db8:1::/64"
				},
				{
					"id": 40,
					"subnet": "2001:db8:2::/64"
				},
				{
					"id": 50,
					"subnet": "2001:db8:3::/64",
					"pools": [
						{
							"pool": "2001:db8:3::100-2001:db8:3::ffff"
						}
					],
					"pd-pools": [
						{
							"prefix": "2001:db8:3:8000::",
							"prefix-len": 48,
							"delegated-len": 64
						}
					],
					// 2 out-of-pool, 1 in-pool host reservations
					// 1 out-of-pool, 1 in-pool prefix reservations
					"reservations": [
						{
							"hw-address": "00:00:00:00:01:23",
							"ip-address": "2001:db8:3::101",
							"prefixes": [ "2001:db8:3:8000::/64" ]
						},
						{
							"hw-address": "00:00:00:00:01:22",
							"ip-address": "2001:db8:3::21",
							"prefixes": [ "2001:db8:2:abcd::/80" ]
						},
						{
							"hw-address": "00:00:00:00:01:23",
							"ip-address": "2001:db8:3::23"
						}
					]
				},
				{
					"id": 60,
					"subnet": "2001:db8:4::/64"
				},
				{
					"id": 70,
					"subnet": "2001:db8:5::/64"
				}
			]
		}
	}`

	return dhcp4, dhcp6
}

// Check creating and shutting down StatsPuller.
func TestStatsPullerBasic(t *testing.T) {
	// Arrange
	db, _, teardown := dbtest.SetupDatabaseTestCase(t)
	defer teardown()
	_ = dbmodel.InitializeSettings(db)
	fa := agentcommtest.NewFakeAgents(nil, nil)

	// Act
	sp, err := NewStatsPuller(db, fa)
	defer sp.Shutdown()

	// Assert
	require.NoError(t, err)
	require.NotEmpty(t, sp.RpsWorker)
}

// Check if Kea response to stat-leaseX-get command is handled correctly when it is
// empty or when stats hooks library is not loaded.  The RPS responses are valid,
// they have their own unit tests in rps_test.go.
func TestStatsPullerEmptyResponse(t *testing.T) {
	// Arrange
	db, _, teardown := dbtest.SetupDatabaseTestCase(t)
	defer teardown()
	_ = dbmodel.InitializeSettings(db)
	_ = createAppWithSubnets(t, db, 0, "", "")

	// prepare fake agents
	keaMock := createKeaMock(func(callNo int) (jsons []string) {
		return []string{
			// simulate empty response
			`[{
				"result": 0,
				"text": "Everything is fine",
				"arguments": {}
			}]`,
			`[{
				"result": 0, "text": "Everything is fine",
				"arguments": {
					"pkt4-ack-sent": [ [ 0, "2019-07-30 10:13:00.000000" ] ]
				}
			}]`,
			// simulate not loaded stat plugin in kea
			`[{
				"result": 2,
				"text": "'stat-lease6-get' command not supported."
			}]`,
			`[{
				"result": 0, "text": "Everything is fine",
				"arguments": {
					"pkt6-reply-sent": [ [ 0, "2019-07-30 10:13:00.000000" ] ]
				}
			}]`,
		}
	})

	fa := agentcommtest.NewFakeAgents(keaMock, nil)

	// prepare stats puller
	sp, _ := NewStatsPuller(db, fa)
	defer sp.Shutdown()

	// Act
	// invoke pulling stats
	err := sp.pullStats()

	// Assert
	require.Error(t, err)
}

// Check if pulling stats works when RPS is included.
// RpsWorker has a thorough set of unit tests so in this
// we verify only that it has entries in its internal
// Map of statistics fetched.  This is enough to demonstrate
// that it is operational.
func checkStatsPullerPullStats(t *testing.T, statsFormat string) {
	// Arrange
	// 1.6 format
	totalAddrs := "total-addreses"
	assignedAddrs := "assigned-addreses"
	declinedAddrs := "declined-addreses"
	if statsFormat == "1.8" {
		totalAddrs = "total-addresses"
		assignedAddrs = "assigned-addresses"
		declinedAddrs = "declined-addresses"
	}

	db, _, teardown := dbtest.SetupDatabaseTestCase(t)
	defer teardown()
	_ = dbmodel.InitializeSettings(db)
	_ = dbmodel.InitializeStats(db)

	// prepare apps with subnets and local subnets
	v4Config, v6Config := createDhcpConfigs()
	app := createAppWithSubnets(t, db, 0, v4Config, v6Config)

	keaMock := createKeaMock(func(callNo int) (jsons []string) {
		return []string{
			fmt.Sprintf(`[{
				"result": 0,
				"text": "Everything is fine",
				"arguments": {
					"result-set": {
						"columns": [ "subnet-id", "%s", "%s", "%s" ],
						"rows": [
							[ 10, 256, 111, 0 ],
							[ 20, 4098, 2034, 4 ]
						],
						"timestamp": "2018-05-04 15:03:37.000000"
					}
				}
			}]`, totalAddrs, assignedAddrs, declinedAddrs),
			`[{
				"result": 0,
				"text": "Everything is fine",
				"arguments": {
					"pkt4-ack-sent": [ [ 44, "2019-07-30 10:13:00.000000" ] ]
				}
			}]`,
			`[{
				"result": 0,
				"text": "Everything is fine",
				"arguments": {
					"result-set": {
						"columns": [ "subnet-id", "total-nas", "assigned-nas", "declined-nas", "total-pds", "assigned-pds" ],
						"rows": [
							[ 30, 4096, 2400, 3, 0, 0],
							[ 40, 0, 0, 0, 1048, 233 ],
							[ 50, 256, 60, 0, 1048, 15 ],
							[ 60, -1, 9223372036854775807, 0, -2, -3 ]
						],
						"timestamp": "2018-05-04 15:03:37.000000"
					}
				}
			}]`,
			`[{
				"result": 0,
				"text": "Everything is fine",
				"arguments": {
					"pkt6-reply-sent": [ [ 66, "2019-07-30 10:13:00.000000" ] ]
				}
			}]`,
		}
	})

	fa := agentcommtest.NewFakeAgents(keaMock, nil)

	for i := range app.Daemons {
		nets, snets, err := detectDaemonNetworks(db, app.Daemons[i])
		require.NoError(t, err)
		_, err = dbmodel.CommitNetworksIntoDB(db, nets, snets, app.Daemons[i])
		require.NoError(t, err)
		hosts, err := detectGlobalHostsFromConfig(db, app.Daemons[i])
		require.NoError(t, err)
		err = dbmodel.CommitGlobalHostsIntoDB(db, hosts, app.Daemons[i], "config")
		require.NoError(t, err)
	}

	// prepare stats puller
	sp, _ := NewStatsPuller(db, fa)
	defer sp.Shutdown()

	// Act
	// invoke pulling stats
	err := sp.pullStats()

	// Assert
	require.NoError(t, err)

	// check collected stats
	localSubnets := []*dbmodel.LocalSubnet{}
	q := db.Model(&localSubnets)
	q = q.Column("local_subnet_id", "stats", "stats_collected_at")
	q = q.Join("INNER JOIN daemon ON local_subnet.daemon_id = daemon.id")
	q = q.Where("daemon.app_id = ?", app.ID)
	err = q.Select()
	require.NoError(t, err)
	snCnt := 0
	for _, sn := range localSubnets {
		switch sn.LocalSubnetID {
		case 10:
			require.Equal(t, uint64(111), sn.Stats["assigned-addresses"])
			require.Equal(t, uint64(0), sn.Stats["declined-addresses"])
			require.Equal(t, uint64(256), sn.Stats["total-addresses"])
			snCnt++
		case 20:
			require.Equal(t, uint64(2034), sn.Stats["assigned-addresses"])
			require.Equal(t, uint64(4), sn.Stats["declined-addresses"])
			require.Equal(t, uint64(4098), sn.Stats["total-addresses"])
			snCnt++
		case 30:
			require.Equal(t, uint64(2400), sn.Stats["assigned-nas"])
			require.Equal(t, uint64(0), sn.Stats["assigned-pds"])
			require.Equal(t, uint64(3), sn.Stats["declined-nas"])
			require.Equal(t, uint64(4096), sn.Stats["total-nas"])
			require.Equal(t, uint64(0), sn.Stats["total-pds"])
			snCnt++
		case 40:
			require.Equal(t, uint64(0), sn.Stats["assigned-nas"])
			require.Equal(t, uint64(233), sn.Stats["assigned-pds"])
			require.Equal(t, uint64(0), sn.Stats["declined-nas"])
			require.Equal(t, uint64(0), sn.Stats["total-nas"])
			require.Equal(t, uint64(1048), sn.Stats["total-pds"])
			snCnt++
		case 50:
			require.Equal(t, uint64(60), sn.Stats["assigned-nas"])
			require.Equal(t, uint64(15), sn.Stats["assigned-pds"])
			require.Equal(t, uint64(0), sn.Stats["declined-nas"])
			require.Equal(t, uint64(256), sn.Stats["total-nas"])
			require.Equal(t, uint64(1048), sn.Stats["total-pds"])
			snCnt++
		case 60:
			require.Equal(t, uint64(math.MaxUint64), sn.Stats["total-nas"])
			require.Equal(t, uint64(math.MaxInt64), sn.Stats["assigned-nas"])
			require.Equal(t, uint64(0), sn.Stats["declined-nas"])
			require.Equal(t, uint64(math.MaxUint64)-1, sn.Stats["total-pds"])
			require.Equal(t, uint64(math.MaxUint64)-2, sn.Stats["assigned-pds"])
			snCnt++
		case 70:
			require.Nil(t, sn.Stats)
		}
	}
	require.Equal(t, 6, snCnt)
	// We should have two rows in RpsWorker.PreviousRps map one for each daemon
	require.Equal(t, 2, len(sp.RpsWorker.PreviousRps))

	// Entry for ID 1 should be dhcp4 daemon, it should have an RPS value of 44
	previous := sp.RpsWorker.PreviousRps[1]
	require.NotEqual(t, nil, previous)
	require.EqualValues(t, 44, previous.Value)

	// Entry for ID 2 should be dhcp6 daemon, it should have an RPS value of 66
	previous = sp.RpsWorker.PreviousRps[2]
	require.NotEqual(t, nil, previous)
	require.EqualValues(t, 66, previous.Value)

	// Check out-of-pool addresses/NAs/PDs utilization
	subnets, _ := dbmodel.GetAllSubnets(db, 0)

	for _, sn := range subnets {
		switch sn.LocalSubnets[0].LocalSubnetID {
		case 10:
			require.InDelta(t, 111.0/256.0, float64(sn.AddrUtilization)/1000.0, 0.001)
			require.Zero(t, sn.PdUtilization)
		case 20:
			require.InDelta(t, 2034.0/(4098.0+2), float64(sn.AddrUtilization)/1000.0, 0.001)
			require.Zero(t, sn.PdUtilization)
		case 30:
			require.InDelta(t, 2400.0/4096.0, float64(sn.AddrUtilization)/1000.0, 0.001)
			require.Zero(t, sn.PdUtilization)
		case 40:
			require.Zero(t, sn.AddrUtilization)
			require.InDelta(t, 233.0/1048.0, float64(sn.PdUtilization)/1000.0, 0.001)
		case 50:
			require.InDelta(t, 60.0/(256.0+2), float64(sn.AddrUtilization)/1000.0, 0.001)
			require.InDelta(t, 15.0/(1048.0+1), float64(sn.PdUtilization)/1000.0, 0.001)
		}
	}

	// Check global statistics
	globals, err := dbmodel.GetAllStats(db)
	require.NoError(t, err)
	require.EqualValues(t, big.NewInt(4358), globals["total-addresses"])
	require.EqualValues(t, big.NewInt(2145), globals["assigned-addresses"])
	require.EqualValues(t, big.NewInt(4), globals["declined-addresses"])
	require.EqualValues(t, big.NewInt(0).Add(
		big.NewInt(4355), big.NewInt(0).SetUint64(math.MaxUint64),
	), globals["total-nas"])
	require.EqualValues(t, big.NewInt(0).Add(
		big.NewInt(2460), big.NewInt(math.MaxInt64),
	), globals["assigned-nas"])
	require.EqualValues(t, big.NewInt(3), globals["declined-nas"])
	require.EqualValues(t, big.NewInt(0).Add(
		big.NewInt(2097), big.NewInt(0).SetUint64(math.MaxUint64),
	), globals["total-pds"])
	require.EqualValues(t, big.NewInt(0).Add(
		big.NewInt(246), big.NewInt(0).SetUint64(math.MaxUint64),
	), globals["assigned-pds"])
}

func TestStatsPullerPullStatsKea16Format(t *testing.T) {
	checkStatsPullerPullStats(t, "1.6")
}

func TestStatsPullerPullStatsKea18Format(t *testing.T) {
	checkStatsPullerPullStats(t, "1.8")
}

// Stork should not attempt to get statistics from  the Kea application without the
// stat_cmds hook library.
func TestGetStatsFromAppWithoutStatCmd(t *testing.T) {
	// Arrange
	db, _, teardown := dbtest.SetupDatabaseTestCase(t)
	defer teardown()
	dbmodel.InitializeSettings(db)

	fa := agentcommtest.NewFakeAgents(nil, nil)

	app := &dbmodel.App{
		ID:   1,
		Type: dbmodel.AppTypeKea,
		Daemons: []*dbmodel.Daemon{
			{
				Active: true,
				Name:   "dhcp4",
				KeaDaemon: &dbmodel.KeaDaemon{
					Config: dbmodel.NewKeaConfig(&map[string]interface{}{
						"Dhcp4": map[string]interface{}{},
					}),
				},
			},
			{
				Active: true,
				Name:   "dhcp6",
				KeaDaemon: &dbmodel.KeaDaemon{
					Config: dbmodel.NewKeaConfig(&map[string]interface{}{
						"Dhcp6": map[string]interface{}{},
					}),
				},
			},
		},
	}

	sp, _ := NewStatsPuller(db, fa)

	// Act
	err := sp.getStatsFromApp(app)

	// Assert
	require.NoError(t, err)
	require.Zero(t, fa.CallNo)
}

// Prepares the Kea configuration file with HA hook and some subnets.
func getHATestConfigWithSubnets(rootName, thisServerName, mode string, peerNames ...string) *dbmodel.KeaConfig {
	haConfig := getHATestConfig(rootName, thisServerName, mode, peerNames...)

	dhcp4, dhcp6 := createDhcpConfigs()
	subnetsConfigRaw := dhcp4
	if rootName == "Dhcp6" {
		subnetsConfigRaw = dhcp6
	}
	subnetsConfig, _ := dbmodel.NewKeaConfigFromJSON(subnetsConfigRaw)

	haHooks := (*haConfig.Map)[rootName].(map[string]interface{})["hooks-libraries"].([]interface{})
	subnetHooks := (*subnetsConfig.Map)[rootName].(map[string]interface{})["hooks-libraries"].([]interface{})

	subnetHooks = append(subnetHooks, haHooks...)

	(*subnetsConfig.Map)[rootName].(map[string]interface{})["hooks-libraries"] = subnetHooks

	return subnetsConfig
}

// Prepares the HA service instances and loads them into database.
// First instance is composed from 3 DHCPv4 daemons and is configured in load
// balancing mode. Second instance is composed from 2 DHCPv6 daemons and is
// configured in hot-standby mode.
func prepareHAEnvironment(t *testing.T, db *pg.DB) (loadBalancing *dbmodel.Service, hotStandby *dbmodel.Service) {
	// Initialize database
	err := dbmodel.InitializeSettings(db)
	require.NoError(t, err)

	err = dbmodel.InitializeStats(db)
	require.NoError(t, err)

	daemons := []*dbmodel.Daemon{}

	// Add machine and app for the primary server.
	m := &dbmodel.Machine{
		ID:        0,
		Address:   "primary",
		AgentPort: 8080,
	}
	err = dbmodel.AddMachine(db, m)
	require.NoError(t, err)
	app := dbmodel.App{
		MachineID: m.ID,
		Type:      dbmodel.AppTypeKea,
		AccessPoints: []*dbmodel.AccessPoint{
			{
				Type:              dbmodel.AccessPointControl,
				Address:           "192.0.2.33",
				Port:              8000,
				Key:               "",
				UseSecureProtocol: true,
			},
		},
		Daemons: []*dbmodel.Daemon{
			{
				Active: true,
				Name:   "dhcp4",
				KeaDaemon: &dbmodel.KeaDaemon{
					Config: getHATestConfigWithSubnets("Dhcp4", "server1", "load-balancing",
						"server1", "server2", "server4"),
					KeaDHCPDaemon: &dbmodel.KeaDHCPDaemon{},
				},
			},
			{
				Active: true,
				Name:   "dhcp6",
				KeaDaemon: &dbmodel.KeaDaemon{
					Config: getHATestConfigWithSubnets("Dhcp6", "server1", "hot-standby",
						"server1", "server2"),
					KeaDHCPDaemon: &dbmodel.KeaDHCPDaemon{},
				},
			},
		},
	}

	_, err = dbmodel.AddApp(db, &app)
	require.NoError(t, err)

	daemons = append(daemons, app.Daemons...)

	// Add the secondary machine.
	m = &dbmodel.Machine{
		ID:        0,
		Address:   "localhost",
		AgentPort: 8080,
	}
	err = dbmodel.AddMachine(db, m)
	require.NoError(t, err)

	app = dbmodel.App{
		MachineID: m.ID,
		Type:      dbmodel.AppTypeKea,
		AccessPoints: []*dbmodel.AccessPoint{
			{
				Type:              dbmodel.AccessPointControl,
				Address:           "192.0.2.66",
				Key:               "",
				Port:              8000,
				UseSecureProtocol: false,
			},
		},
		Daemons: []*dbmodel.Daemon{
			{
				Active: true,
				Name:   "dhcp4",
				KeaDaemon: &dbmodel.KeaDaemon{
					Config: getHATestConfigWithSubnets("Dhcp4", "server2", "load-balancing",
						"server1", "server2", "server4"),
					KeaDHCPDaemon: &dbmodel.KeaDHCPDaemon{},
				},
			},
			{
				Active: true,
				Name:   "dhcp6",
				KeaDaemon: &dbmodel.KeaDaemon{
					Config: getHATestConfigWithSubnets("Dhcp6", "server2", "hot-standby",
						"server1", "server2"),
					KeaDHCPDaemon: &dbmodel.KeaDHCPDaemon{},
				},
			},
		},
	}
	_, err = dbmodel.AddApp(db, &app)
	require.NoError(t, err)

	daemons = append(daemons, app.Daemons...)

	// Add machine and app for the DHCPv4 backup server.
	m = &dbmodel.Machine{
		ID:        0,
		Address:   "backup1",
		AgentPort: 8080,
	}
	err = dbmodel.AddMachine(db, m)
	require.NoError(t, err)

	app = dbmodel.App{
		MachineID: m.ID,
		Type:      dbmodel.AppTypeKea,
		AccessPoints: []*dbmodel.AccessPoint{
			{
				Type:              dbmodel.AccessPointControl,
				Address:           "192.0.2.133",
				Key:               "",
				Port:              8000,
				UseSecureProtocol: false,
			},
		},
		Daemons: []*dbmodel.Daemon{
			{
				Name:   "dhcp4",
				Active: true,
				KeaDaemon: &dbmodel.KeaDaemon{
					Config: getHATestConfigWithSubnets("Dhcp4", "server4", "load-balancing",
						"server1", "server2", "server4"),
					KeaDHCPDaemon: &dbmodel.KeaDHCPDaemon{},
				},
			},
		},
	}
	_, err = dbmodel.AddApp(db, &app)
	require.NoError(t, err)

	daemons = append(daemons, app.Daemons...)

	// Detect HA services
	for _, daemon := range daemons {
		services := DetectHAServices(db, daemon)
		err = dbmodel.CommitServicesIntoDB(db, services, daemon)
		require.NoError(t, err)
	}

	// There should be two services returned, one for DHCPv4 and one for DHCPv6.
	services, err := dbmodel.GetDetailedAllServices(db)
	require.NoError(t, err)
	require.Len(t, services, 2)

	for _, service := range services {
		innerService := service
		switch service.HAService.HAMode {
		case "load-balancing":
			loadBalancing = &innerService
		case "hot-standby":
			hotStandby = &innerService
		}
	}

	require.NotNil(t, loadBalancing)
	require.NotNil(t, hotStandby)

	for _, daemon := range daemons {
		nets, snets, err := detectDaemonNetworks(db, daemon)
		require.NoError(t, err)
		_, err = dbmodel.CommitNetworksIntoDB(db, nets, snets, daemon)
		require.NoError(t, err)
		hosts, err := detectGlobalHostsFromConfig(db, daemon)
		require.NoError(t, err)
		err = dbmodel.CommitGlobalHostsIntoDB(db, hosts, daemon, "config")
		require.NoError(t, err)
	}

	return loadBalancing, hotStandby
}

func TestGetHATestConfigWithSubnets(t *testing.T) {
	// Act
	config := getHATestConfigWithSubnets("Dhcp4", "server1", "hot-standby", "server2", "server4")

	// Assert
	require.NotNil(t, config)
	path, params, ok := config.GetHAHooksLibrary()
	require.True(t, ok)
	require.NotEmpty(t, path)
	require.Equal(t, "server1", *params.ThisServerName)
	var subnets []interface{}
	err := config.DecodeTopLevelSubnets(&subnets)
	require.NoError(t, err)
	require.NotEmpty(t, subnets)
}

func TestPrepareHAEnvironment(t *testing.T) {
	// Arrange
	db, _, teardown := dbtest.SetupDatabaseTestCase(t)
	defer teardown()

	// Act
	loadBalancing, hotStandby := prepareHAEnvironment(t, db)
	keaMock := createKeaMock(func(callNo int) (jsons []string) { return []string{} })

	fa := agentcommtest.NewFakeAgents(keaMock, nil)
	sp, err := NewStatsPuller(db, fa)

	// Assert
	require.NoError(t, err)
	require.NotNil(t, sp)
	require.NotNil(t, loadBalancing)
	require.NotNil(t, hotStandby)
}

func TestStatsPullerPullStatsHAPairHealthy(t *testing.T) {
	// Arrange
	db, _, teardown := dbtest.SetupDatabaseTestCase(t)
	defer teardown()

	loadBalancing, hotStandby := prepareHAEnvironment(t, db)

	shiftFactor := 100

	keaMock := createKeaMock(func(callNo int) []string {
		shift := int64(callNo * shiftFactor)
		totalShift := shift * 2
		data := []interface{}{
			[]StatLeaseGetResponse{
				{
					ResponseHeader: keactrl.ResponseHeader{
						Result: 0,
						Text:   "Everything is fine",
					},
					Arguments: &StatLeaseGetArgs{
						ResultSet: ResultSetInStatLeaseGet{
							Columns: []string{"subnet-id", "total-addreses", "assigned-addreses", "declined-addreses"},
							Rows: [][]int64{
								{10, 256 + totalShift, 111 + shift, 0 + shift},
								{20, 4098 + totalShift, 2034 + shift, 4 + shift},
							},
						},
						Timestamp: "2018-05-04 15:03:37.000000",
					},
				},
			},
			[]StatGetResponse4{
				{
					ResponseHeader: keactrl.ResponseHeader{
						Result: 0,
						Text:   "Everything is fine",
					},
					Arguments: &ResponseArguments4{
						Samples: []interface{}{
							[]interface{}{44, "2019-07-30 10:13:00.000000"},
						},
					},
				},
			},
			[]StatLeaseGetResponse{
				{
					ResponseHeader: keactrl.ResponseHeader{
						Result: 0,
						Text:   "Everything is fine",
					},
					Arguments: &StatLeaseGetArgs{
						ResultSet: ResultSetInStatLeaseGet{
							Columns: []string{"subnet-id", "total-nas", "assigned-nas", "declined-nas", "total-pds", "assigned-pds"},
							Rows: [][]int64{
								{30, 4096 + totalShift, 2400 + shift, 3 + shift, 0 + totalShift, 0 + shift},
								{40, 0 + totalShift, 0 + shift, 0 + shift, 1048 + totalShift, 233 + shift},
								{50, 256 + totalShift, 60 + shift, 0 + shift, 1048 + totalShift, 15 + shift},
								{60, -1, 9223372036854775807, 0, -2, -3},
							},
						},
						Timestamp: "2018-05-04 15:03:37.000000",
					},
				},
			},
			[]StatGetResponse6{
				{
					ResponseHeader: keactrl.ResponseHeader{
						Result: 0,
						Text:   "Everything is fine",
					},
					Arguments: &ResponseArguments6{
						Samples: []interface{}{
							[]interface{}{66, "2019-07-30 10:13:00.000000"},
						},
					},
				},
			},
		}

		var jsons []string

		for _, item := range data {
			j, _ := json.Marshal(item)
			jsons = append(jsons, string(j))
		}

		return jsons
	})

	fa := agentcommtest.NewFakeAgents(keaMock, nil)

	// prepare stats puller
	sp, err := NewStatsPuller(db, fa)
	require.NoError(t, err)
	defer sp.Shutdown()

	// Act
	err = sp.pullStats()

	// Assert
	require.NoError(t, err)
	require.NotNil(t, loadBalancing)
	require.NotNil(t, hotStandby)

	// Check collected stats in the local subnets. There is no meaning if they
	// are from the HA daemons.
	localSubnets := []*dbmodel.LocalSubnet{}
	q := db.Model(&localSubnets)
	q = q.Relation("Daemon")
	err = q.Select()
	require.NoError(t, err)
	snCnt := 0
	for _, sn := range localSubnets {
		shift := (sn.Daemon.AppID - 1) * int64(shiftFactor)
		totalShift := shift * 2
		switch sn.LocalSubnetID {
		case 10:
			require.Equal(t, uint64(111+shift), sn.Stats["assigned-addresses"])
			require.Equal(t, uint64(0+shift), sn.Stats["declined-addresses"])
			require.Equal(t, uint64(256+totalShift), sn.Stats["total-addresses"])
			snCnt++
		case 20:
			require.Equal(t, uint64(2034+shift), sn.Stats["assigned-addresses"])
			require.Equal(t, uint64(4+shift), sn.Stats["declined-addresses"])
			require.Equal(t, uint64(4098+totalShift), sn.Stats["total-addresses"])
			snCnt++
		case 30:
			require.Equal(t, uint64(2400+shift), sn.Stats["assigned-nas"])
			require.Equal(t, uint64(0+shift), sn.Stats["assigned-pds"])
			require.Equal(t, uint64(3+shift), sn.Stats["declined-nas"])
			require.Equal(t, uint64(4096+totalShift), sn.Stats["total-nas"])
			require.Equal(t, uint64(0+totalShift), sn.Stats["total-pds"])
			snCnt++
		case 40:
			require.Equal(t, uint64(0+shift), sn.Stats["assigned-nas"])
			require.Equal(t, uint64(233+shift), sn.Stats["assigned-pds"])
			require.Equal(t, uint64(0+shift), sn.Stats["declined-nas"])
			require.Equal(t, uint64(0+totalShift), sn.Stats["total-nas"])
			require.Equal(t, uint64(1048+totalShift), sn.Stats["total-pds"])
			snCnt++
		case 50:
			require.Equal(t, uint64(60+shift), sn.Stats["assigned-nas"])
			require.Equal(t, uint64(15+shift), sn.Stats["assigned-pds"])
			require.Equal(t, uint64(0+shift), sn.Stats["declined-nas"])
			require.Equal(t, uint64(256+totalShift), sn.Stats["total-nas"])
			require.Equal(t, uint64(1048+totalShift), sn.Stats["total-pds"])
			snCnt++
		case 60:
			require.Equal(t, uint64(math.MaxUint64), sn.Stats["total-nas"])
			require.Equal(t, uint64(math.MaxInt64), sn.Stats["assigned-nas"])
			require.Equal(t, uint64(0), sn.Stats["declined-nas"])
			require.Equal(t, uint64(math.MaxUint64)-1, sn.Stats["total-pds"])
			require.Equal(t, uint64(math.MaxUint64)-2, sn.Stats["assigned-pds"])
			snCnt++
		case 70:
			require.Nil(t, sn.Stats)
		}
	}

	// 3 DHCPv4 daemons with 2 local subnets.
	// 2 DHCPv6 daemons with 4 local subnets.
	require.Equal(t, 3*2+2*4, snCnt)

	// Check the subnet utilizations.
	subnets, err := dbmodel.GetAllSubnets(db, 0)
	require.NoError(t, err)
	require.Len(t, subnets, 7)

	for _, sn := range subnets {
		switch sn.Prefix {
		case "192.0.2.0/24":
			require.InDelta(t, 111.0/256.0, float64(sn.AddrUtilization)/1000.0, 0.001)
			require.Zero(t, sn.PdUtilization)
		case "192.0.3.0/26":
			require.InDelta(t, 2034.0/(4098.0+2), float64(sn.AddrUtilization)/1000.0, 0.001)
			require.Zero(t, sn.PdUtilization)
		case "2001:db8:1::/64":
			require.InDelta(t, 2400.0/4096.0, float64(sn.AddrUtilization)/1000.0, 0.001)
			require.Zero(t, sn.PdUtilization)
		case "2001:db8:2::/64":
			require.Zero(t, sn.AddrUtilization)
			require.InDelta(t, 233.0/1048.0, float64(sn.PdUtilization)/1000.0, 0.001)
		case "2001:db8:3::/64":
			require.InDelta(t, 60.0/(256.0+2), float64(sn.AddrUtilization)/1000.0, 0.001)
			require.InDelta(t, 15.0/(1048.0+1), float64(sn.PdUtilization)/1000.0, 0.001)
		}
	}

	// Check global statistics
	globals, err := dbmodel.GetAllStats(db)
	require.NoError(t, err)
	require.EqualValues(t, big.NewInt(4358), globals["total-addresses"])
	require.EqualValues(t, big.NewInt(2145), globals["assigned-addresses"])
	require.EqualValues(t, big.NewInt(4), globals["declined-addresses"])
	require.EqualValues(t, big.NewInt(0).Add(
		big.NewInt(4355), big.NewInt(0).SetUint64(math.MaxUint64),
	), globals["total-nas"])
	require.EqualValues(t, big.NewInt(0).Add(
		big.NewInt(2460), big.NewInt(math.MaxInt64),
	), globals["assigned-nas"])
	require.EqualValues(t, big.NewInt(3), globals["declined-nas"])
	require.EqualValues(t, big.NewInt(0).Add(
		big.NewInt(2097), big.NewInt(0).SetUint64(math.MaxUint64),
	), globals["total-pds"])
	require.EqualValues(t, big.NewInt(0).Add(
		big.NewInt(246), big.NewInt(0).SetUint64(math.MaxUint64),
	), globals["assigned-pds"])
}

func TestStatsPullerPullStatsHAPairPrimaryIsDownSecondaryIsReady(t *testing.T) {
	// TBD
}

func TestStatsPullerPullStatsHAPairPrimaryIsDownSecondaryIsDown(t *testing.T) {
	// TBD
}
