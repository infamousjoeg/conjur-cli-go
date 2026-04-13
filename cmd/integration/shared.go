//go:build integration
// +build integration

package main

import (
	"bytes"
	"fmt"
	"io"
	"os"
	"os/exec"
	"regexp"
	"strings"
	"testing"

	"github.com/cyberark/conjur-api-go/conjurapi"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const (
	pathToBinary              = "conjur"
	integrationDefaultAccount = "dev"
	adminPasswordEnvVar       = "CONJUR_ADMIN_PASSWORD"

	insecureModeWarning = "Warning: Running the command with '--insecure' makes your system vulnerable to security attacks\n" +
		"If you prefer to communicate with the server securely you must reinitialize the client in secure mode.\n"
	selfSignedWarning = "Warning: Using self-signed certificates is not recommended and could lead to exposure of sensitive data\n"

	testPolicy = `
- !variable meow
- !variable woof
- !user alice
- !host bob

- !permit
  resource: !variable meow
  role: !user alice
  privileges: [ read ]

- !policy
  id: conjur/authn-iam/prod
  body:
    - !webservice
    - !group clients
    - !permit
      role: !group clients
      privilege: [ read, authenticate ]
      resource: !webservice
`
	emptyPolicy = `#`
)

func newConjurTestCLI(t *testing.T) (cli *testConjurCLI) {
	homeDir := t.TempDir()
	account := integrationAccount()

	cli = &testConjurCLI{
		homeDir: homeDir,
		account: account,
	}

	return
}

type testConjurCLI struct {
	homeDir string
	account string
}

func (cli *testConjurCLI) InitAndLoginAsAdmin(t *testing.T) {
	cli.InitAndLoginAsAdminWithPolicy(t, testPolicy)
}

func (cli *testConjurCLI) InitAndLoginAsAdminWithPolicy(t *testing.T, policyText string) {
	// Initialize the CLI
	cli.Init(t)

	// Login as admin
	cli.LoginAsAdmin(t)

	// Load requested root policy
	cli.ReplacePolicy(t, policyText)
}

func (cli *testConjurCLI) Logout(t *testing.T) {
	stdOut, stdErr, err := cli.Run("logout")
	assertLogoutCmd(t, err, stdOut, stdErr)
}

func (cli *testConjurCLI) Init(t *testing.T) {
	stdOut, _, err := cli.Run("init", string(conjurapi.EnvironmentSH), "-a", cli.account, "-u", "http://conjur", "-i", "--force-netrc", "--force")
	assertInitCmd(t, err, stdOut, cli.homeDir)
}

func (cli *testConjurCLI) InitCloud(t *testing.T) {
	stdOut, _, err := cli.Run("init", string(conjurapi.EnvironmentSaaS), "-u", "https://tenant.secretsmgr.cyberark.cloud", "--ca-cert", "conjur-server.pem", "--force")
	assertInitCmd(t, err, stdOut, cli.homeDir)
}

func (cli *testConjurCLI) InitWithTrailingSlash(t *testing.T) {
	stdOut, _, err := cli.Run("init", string(conjurapi.EnvironmentSH), "-a", cli.account, "-u", "http://conjur/", "-i", "--force-netrc", "--force")
	assertInitCmd(t, err, stdOut, cli.homeDir)
}

func (cli *testConjurCLI) LoginAsAdmin(t *testing.T) {
	stdOut, stdErr, err := cli.Run("login", "-i", "admin", "-p", adminPassword(t))
	assertLoginCmd(t, err, stdOut, stdErr)
}

func (cli *testConjurCLI) LoginAsHost(t *testing.T, host string) {
	hostAPIKey := cli.rotateRoleAPIKey(t, "host", fmt.Sprintf("%s:host:%s", cli.account, host))
	stdOut, stdErr, err := cli.Run("login", "-i", "host/"+host, "-p", hostAPIKey)
	assertLoginCmd(t, err, stdOut, stdErr)
}

func (cli *testConjurCLI) LoginAsUser(t *testing.T, user string) {
	userAPIKey := cli.rotateRoleAPIKey(t, "user", fmt.Sprintf("%s:user:%s", cli.account, user))
	stdOut, stdErr, err := cli.Run("login", "-i", user, "-p", userAPIKey)
	assertLoginCmd(t, err, stdOut, stdErr)
}

func (cli *testConjurCLI) LoadPolicy(t *testing.T, policyText string) {
	stdOut, stdErr, err := cli.RunWithStdin(
		bytes.NewReader([]byte(policyText)),
		"policy", "load", "-b", "root", "-f", "-",
	)
	assertPolicyLoadCmd(t, err, stdOut, stdErr)
}

func (cli *testConjurCLI) ReplacePolicy(t *testing.T, policyText string) {
	stdOut, stdErr, err := cli.RunWithStdin(
		bytes.NewReader([]byte(policyText)),
		"policy", "replace", "-b", "root", "-f", "-",
	)
	assertPolicyLoadCmd(t, err, stdOut, stdErr)
}

func (cli *testConjurCLI) LoadPolicyFile(t *testing.T, policyFile string) {
	policy, err := os.ReadFile(policyFile)
	require.NoError(t, err)
	cli.LoadPolicy(t, string(policy))
}

func (cli *testConjurCLI) CreateSecret(t *testing.T, variable string, value string) {
	stdOut, stdErr, err := cli.Run("variable", "set", "-i", variable, "-v", value)
	assertSetVariableCmd(t, err, stdOut, stdErr)
}

func (cli *testConjurCLI) DryRunPolicy(t *testing.T, mode string, branch string, policyText string) (stdOut string, stdErr string, err error) {
	return cli.RunWithStdin(
		bytes.NewReader([]byte(policyText)),
		"policy", mode, "--dry-run", "-b", branch, "-f", "-",
	)
}

func (cli *testConjurCLI) RunWithStdin(stdIn io.Reader, args ...string) (stdOut string, stdErr string, err error) {
	cmd := exec.Command(pathToBinary, args...)
	stdOutBuffer := new(bytes.Buffer)
	stdErrBuffer := new(bytes.Buffer)
	cmd.Stdin = stdIn
	cmd.Stdout = io.MultiWriter(stdOutBuffer, os.Stdout)
	cmd.Stderr = io.MultiWriter(stdErrBuffer, os.Stderr)

	cmd.Env = append(cmd.Env, "HOME="+cli.homeDir)

	err = cmd.Run()
	return stdOutBuffer.String(), stdErrBuffer.String(), err
}

func (cli *testConjurCLI) Run(args ...string) (stdOut string, stdErr string, err error) {
	return cli.RunWithStdin(nil, args...)
}

func (cli *testConjurCLI) rotateRoleAPIKey(t *testing.T, roleType string, roleID string) string {
	stdOut, stdErr, err := cli.Run(roleType, "rotate-api-key", "-i", roleID)
	require.NoError(t, err)
	require.Empty(t, stdErr)
	apiKey := strings.TrimSpace(stdOut)
	require.NotEmpty(t, apiKey)
	return apiKey
}

func integrationAccount() string {
	account := strings.TrimSpace(os.Getenv("CONJUR_ACCOUNT"))
	if account == "" {
		return integrationDefaultAccount
	}
	return account
}

func adminPassword(t *testing.T) string {
	password := strings.TrimSpace(os.Getenv(adminPasswordEnvVar))
	if password == "" {
		password = strings.TrimSpace(os.Getenv("ADMIN_INITIAL_PASSWORD"))
	}
	require.NotEmpty(t, password, adminPasswordEnvVar+" must be set for integration tests")
	return password
}

func assertInitCmd(t *testing.T, err error, stdOut string, homeDir string) {
	assert.NoError(t, err)
	assert.Contains(t, stdOut, "Wrote configuration to "+homeDir+"/.conjurrc\n")
}

func assertLoginCmd(t *testing.T, err error, stdOut string, stdErr string) {
	assert.NoError(t, err)
	assert.Contains(t, stdOut, "Logged in\n")
	assert.Equal(t, "", stdErr)
}

func assertWhoamiCmd(t *testing.T, err error, stdOut string, stdErr string) {
	assert.NoError(t, err)
	assert.Contains(t, stdOut, "token_issued_at")
	assert.Contains(t, stdOut, "client_ip")
	assert.Contains(t, stdOut, "user_agent")
	assert.Contains(t, stdOut, "account")
	assert.Contains(t, stdOut, "username")
	assert.Equal(t, "", stdErr)
}

func assertAuthenticateCmd(t *testing.T, err error, stdOut string, stdErr string) {
	assert.NoError(t, err)
	assert.Contains(t, stdOut, "protected")
	assert.Contains(t, stdOut, "payload")
	assert.Contains(t, stdOut, "signature")
	assert.Equal(t, "", stdErr)
}

func assertNotFound(t *testing.T, err error, stdOut string, stdErr string) {
	assert.Error(t, err)
	assert.Equal(t, "", stdOut)
	assert.Contains(t, stdErr, "404 Not Found.")
}

func assertPolicyLoadCmd(t *testing.T, err error, stdOut string, stdErr string) {
	require.NoError(t, err)
	assert.Contains(t, stdOut, "created_roles")
	assert.Contains(t, stdOut, "version")
	assert.Equal(t, "Loaded policy 'root'\n", stdErr)
}

func assertPolicyValidateSuccessCmd(t *testing.T, err error, stdOut string, stdErr string) {
	require.NoError(t, err)
	assert.Contains(t, stdOut, "Valid YAML")
}

func assertPolicyValidateInvalidCmd(t *testing.T, err error, stdOut string, stdErr string) {
	require.NoError(t, err)
	assert.Contains(t, stdOut, "Invalid YAML")
}

func assertSetVariableCmd(t *testing.T, err error, stdOut string, stdErr string) {
	assert.NoError(t, err)
	assert.Equal(t, "Value added\n", stdOut)
	assert.Equal(t, "", stdErr)
}

func assertGetVariableCmd(t *testing.T, err error, stdOut string, stdErr string, excpectedValue string) {
	assert.NoError(t, err)
	assert.Equal(t, excpectedValue+"\n", stdOut)
	assert.Equal(t, "", stdErr)
}

func assertGetTwoVariablesCmd(t *testing.T, err error, stdOut string, stdErr string) {
	assert.NoError(t, err)
	assert.Contains(t, stdOut, "moo")
	assert.Contains(t, stdOut, "quack")
	assert.Equal(t, "", stdErr)
}

func assertExistsCmd(t *testing.T, err error, stdOut string, stdErr string) {
	assert.NoError(t, err)
	assert.Equal(t, "false\n", stdOut)
}

func assertLogoutCmd(t *testing.T, err error, stdOut string, stdErr string) {
	assert.NoError(t, err)
	assert.Contains(t, stdOut, "Logged out\n")
	assert.Equal(t, "", stdErr)
}

func assertAPIKeyRotationCmd(t *testing.T, err error, stdOut string, stdErr string, priorAPIKey string) {
	assert.NoError(t, err)
	assert.Regexp(t, regexp.MustCompile("[a-zA-Z0-9]{45,60}\n"), stdOut)
	assert.NotEqual(t, priorAPIKey, stdOut)
	assert.Equal(t, "", stdErr)
}
