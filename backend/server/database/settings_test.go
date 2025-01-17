package dbops

import (
	"net"
	"os"
	"path"
	"testing"

	"github.com/stretchr/testify/require"
	"isc.org/stork/testutil"
)

// Test that connection string is created when all parameters are specified and
// none of the values include a space character. Also, make sure that the password
// with upper case letters is handled correctly.
func TestConvertToConnectionStringNoSpaces(t *testing.T) {
	settings := DatabaseSettings{
		DBName:   "stork",
		User:     "admin",
		Password: "StOrK123",
		Host:     "localhost",
		Port:     123,
	}

	params := settings.ConvertToConnectionString()
	require.Equal(t, "dbname='stork' user='admin' password='StOrK123' host='localhost' port=123 sslmode='disable'", params)
}

// Test that the password including space character is enclosed in quotes.
func TestConvertToConnectionStringWithSpaces(t *testing.T) {
	settings := DatabaseSettings{
		DBName:   "stork",
		User:     "admin",
		Password: "StOrK123 567",
		Host:     "localhost",
		Port:     123,
	}

	params := settings.ConvertToConnectionString()
	require.Equal(t, "dbname='stork' user='admin' password='StOrK123 567' host='localhost' port=123 sslmode='disable'", params)
}

// Test that quotes and double quotes are escaped.
func TestConvertToConnectionStringWithEscapes(t *testing.T) {
	settings := DatabaseSettings{
		DBName:   "stork",
		User:     "admin",
		Password: `StOrK123'56"7`,
		Host:     "localhost",
		Port:     123,
	}

	params := settings.ConvertToConnectionString()
	require.Equal(t, `dbname='stork' user='admin' password='StOrK123\'56\"7' host='localhost' port=123 sslmode='disable'`, params)
}

// Test that when the host is not specified it is not included in the connection
// string.
func TestConvertToConnectionStringWithOptionalHost(t *testing.T) {
	settings := DatabaseSettings{
		DBName:   "stork",
		User:     "admin",
		Password: "StOrK123 567",
		Port:     123,
	}

	params := settings.ConvertToConnectionString()
	require.Equal(t, "dbname='stork' user='admin' password='StOrK123 567' port=123 sslmode='disable'", params)
}

// Test that when the port is 0, it is not included in the connection string.
func TestConvertToConnectionStringWithOptionalPort(t *testing.T) {
	settings := DatabaseSettings{
		DBName:   "stork",
		User:     "admin",
		Password: "stork",
		Host:     "localhost",
	}

	params := settings.ConvertToConnectionString()
	require.Equal(t, "dbname='stork' user='admin' password='stork' host='localhost' sslmode='disable'", params)
}

// Test that sslmode and related parameters are included in the connection string.
func TestConvertToConnectionStringWithSSLMode(t *testing.T) {
	settings := DatabaseSettings{
		DBName:      "stork",
		User:        "admin",
		Password:    "stork",
		SSLMode:     "require",
		SSLCert:     "/tmp/sslcert",
		SSLKey:      "/tmp/sslkey",
		SSLRootCert: "/tmp/sslroot.crt",
	}

	params := settings.ConvertToConnectionString()
	require.Equal(t, "dbname='stork' user='admin' password='stork' sslmode='require' sslcert='/tmp/sslcert' sslkey='/tmp/sslkey' sslrootcert='/tmp/sslroot.crt'", params)
}

// Test that convertToPgOptions function returns the default (empty) unix
// socket if the host is not provided.
func TestConvertToPgOptionsWithDefaultHost(t *testing.T) {
	// Arrange
	settings := DatabaseSettings{}

	// Act
	params, _ := settings.convertToPgOptions()

	// Assert
	require.Empty(t, params.Addr)
	require.EqualValues(t, "unix", params.Network)
}

// Test that convertToPgOptions function outputs SSL related parameters.
func TestConvertToPgOptionsWithSSLMode(t *testing.T) {
	sb := testutil.NewSandbox()
	defer sb.Close()

	serverCert, serverKey, _, err := testutil.CreateTestCerts(sb)
	require.NoError(t, err)

	settings := DatabaseSettings{
		Host:     "http://postgres",
		DBName:   "stork",
		User:     "admin",
		Password: "stork",
		SSLMode:  "require",
		SSLCert:  serverCert,
		SSLKey:   serverKey,
	}

	params, _ := settings.convertToPgOptions()
	require.NotNil(t, params)
	require.NotNil(t, params.TLSConfig)

	require.True(t, params.TLSConfig.InsecureSkipVerify)
	require.Nil(t, params.TLSConfig.VerifyConnection)
	require.Empty(t, params.TLSConfig.ServerName)
}

// Test that ConvertToPgOptions function fails when there is an error in the
// SSL specific configuration.
func TestConvertToPgOptionsWithWrongSSLModeSettings(t *testing.T) {
	sb := testutil.NewSandbox()
	defer sb.Close()

	settings := DatabaseSettings{
		Host:     "http://postgres",
		DBName:   "stork",
		User:     "admin",
		Password: "stork",
		SSLMode:  "unsupported",
	}

	params, err := settings.convertToPgOptions()
	require.Nil(t, params)
	require.Error(t, err)
}

// Test that the TCP network kind is recognized properly.
func TestConvertToPgOptionsTCP(t *testing.T) {
	// Arrange
	settings := DatabaseSettings{
		DBName:   "stork",
		User:     "admin",
		Password: "StOrK123",
		Port:     123,
	}

	hosts := []string{"localhost", "192.168.0.1", "fe80::42", "foo.bar"}

	for _, host := range hosts {
		settings.Host = host

		t.Run("host", func(t *testing.T) {
			// Act
			options, err := settings.convertToPgOptions()

			// Assert
			require.NoError(t, err)
			require.EqualValues(t, "tcp", options.Network)
		})
	}
}

// Test that the socket is recognized properly.
func TestConvertToPgOptionsSocket(t *testing.T) {
	// Arrange
	// Open a socket.
	socketDir := os.TempDir()
	socketPath := path.Join(socketDir, ".s.PGSQL.123")
	listener, _ := net.Listen("unix", socketPath)
	defer listener.Close()

	settings := DatabaseSettings{
		DBName:   "stork",
		Host:     socketDir,
		User:     "admin",
		Password: "StOrK123",
		Port:     123,
	}

	// Act
	options, err := settings.convertToPgOptions()

	// Assert
	require.NoError(t, err)
	require.EqualValues(t, "unix", options.Network)
}

// Test that the string is converted into the logging query preset properly.
func TestNewLoggingQueryPreset(t *testing.T) {
	require.EqualValues(t, LoggingQueryPresetAll, newLoggingQueryPreset("all"))
	require.EqualValues(t, LoggingQueryPresetRuntime, newLoggingQueryPreset("run"))
	require.EqualValues(t, LoggingQueryPresetNone, newLoggingQueryPreset("none"))
	require.EqualValues(t, LoggingQueryPresetNone, newLoggingQueryPreset(""))
	require.EqualValues(t, LoggingQueryPresetNone, newLoggingQueryPreset("nil"))
	require.EqualValues(t, LoggingQueryPresetNone, newLoggingQueryPreset("false"))
}
