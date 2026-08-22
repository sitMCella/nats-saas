// Package testnats boots an in-process, operator-mode nats-server for tests
// in other internal packages (pool, kvstore), minting real per-tenant
// Account/User JWTs and seeds — the same JWT+seed auth model production
// uses (docs/tenant-provisioning/design.md §6), not a stand-in credential
// scheme. It exists purely to back tests and is never imported by
// production code.
package testnats

import (
	"testing"
	"time"

	"github.com/nats-io/jwt/v2"
	natsserver "github.com/nats-io/nats-server/v2/server"
	"github.com/nats-io/nkeys"
)

// Creds is one tenant's real NATS user credentials.
type Creds struct {
	JWT, Seed string
}

// Options configures StartServer.
type Options struct {
	// JetStream enables JetStream on the server and grants each minted
	// account unlimited JetStream storage, so KV buckets can be created.
	JetStream bool
}

// StartServer boots a server with one Account per name in tenantNames and
// returns its client URL plus each tenant's user credentials. The server is
// shut down automatically via t.Cleanup.
func StartServer(t *testing.T, opts Options, tenantNames ...string) (url string, creds map[string]Creds) {
	t.Helper()

	opKP, err := nkeys.CreateOperator()
	if err != nil {
		t.Fatalf("create operator key: %v", err)
	}
	opPub, err := opKP.PublicKey()
	if err != nil {
		t.Fatalf("operator public key: %v", err)
	}
	resolver := &natsserver.MemAccResolver{}

	// A SYS account is required for JetStream to set up its internal
	// subscriptions in operator/trusted-JWT mode, mirroring the SYS account
	// docs/nats-cluster/design.md §4 bootstraps for the real cluster.
	sysKP, err := nkeys.CreateAccount()
	if err != nil {
		t.Fatalf("create system account key: %v", err)
	}
	sysPub, err := sysKP.PublicKey()
	if err != nil {
		t.Fatalf("system account public key: %v", err)
	}
	sysClaims := jwt.NewAccountClaims(sysPub)
	sysClaims.Name = "SYS"

	opClaims := jwt.NewOperatorClaims(opPub)
	opClaims.SystemAccount = sysPub
	opJWT, err := opClaims.Encode(opKP)
	if err != nil {
		t.Fatalf("encode operator claims: %v", err)
	}
	decodedOp, err := jwt.DecodeOperatorClaims(opJWT)
	if err != nil {
		t.Fatalf("decode operator claims: %v", err)
	}

	sysJWT, err := sysClaims.Encode(opKP)
	if err != nil {
		t.Fatalf("encode system account claims: %v", err)
	}
	if err := resolver.Store(sysPub, sysJWT); err != nil {
		t.Fatalf("store system account jwt: %v", err)
	}

	creds = make(map[string]Creds, len(tenantNames))

	for _, name := range tenantNames {
		acctKP, err := nkeys.CreateAccount()
		if err != nil {
			t.Fatalf("create account key: %v", err)
		}
		acctPub, err := acctKP.PublicKey()
		if err != nil {
			t.Fatalf("account public key: %v", err)
		}
		acctClaims := jwt.NewAccountClaims(acctPub)
		acctClaims.Name = name
		if opts.JetStream {
			acctClaims.Limits.JetStreamLimits.MemoryStorage = jwt.NoLimit
			acctClaims.Limits.JetStreamLimits.DiskStorage = jwt.NoLimit
			acctClaims.Limits.JetStreamLimits.Streams = jwt.NoLimit
			acctClaims.Limits.JetStreamLimits.Consumer = jwt.NoLimit
		}
		acctJWT, err := acctClaims.Encode(opKP)
		if err != nil {
			t.Fatalf("encode account claims: %v", err)
		}
		if err := resolver.Store(acctPub, acctJWT); err != nil {
			t.Fatalf("store account jwt: %v", err)
		}

		userKP, err := nkeys.CreateUser()
		if err != nil {
			t.Fatalf("create user key: %v", err)
		}
		userPub, err := userKP.PublicKey()
		if err != nil {
			t.Fatalf("user public key: %v", err)
		}
		userClaims := jwt.NewUserClaims(userPub)
		userJWT, err := userClaims.Encode(acctKP)
		if err != nil {
			t.Fatalf("encode user claims: %v", err)
		}
		seed, err := userKP.Seed()
		if err != nil {
			t.Fatalf("user seed: %v", err)
		}

		creds[name] = Creds{JWT: userJWT, Seed: string(seed)}
	}

	serverOpts := &natsserver.Options{
		Host:             "127.0.0.1",
		Port:             -1,
		TrustedOperators: []*jwt.OperatorClaims{decodedOp},
		AccountResolver:  resolver,
		SystemAccount:    sysPub,
		NoLog:            true,
		NoSigs:           true,
	}
	if opts.JetStream {
		serverOpts.JetStream = true
		serverOpts.StoreDir = t.TempDir()
	}

	srv, err := natsserver.NewServer(serverOpts)
	if err != nil {
		t.Fatalf("create test server: %v", err)
	}
	srv.ConfigureLogger()
	srv.Start()
	if !srv.ReadyForConnections(5 * time.Second) {
		t.Fatal("test server did not become ready")
	}
	t.Cleanup(srv.Shutdown)

	return srv.ClientURL(), creds
}
