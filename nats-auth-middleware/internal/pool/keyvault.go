package pool

import (
	"context"
	"errors"
	"fmt"
	"net/http"

	"github.com/Azure/azure-sdk-for-go/sdk/azcore"
	"github.com/Azure/azure-sdk-for-go/sdk/security/keyvault/azsecrets"
)

// KeyVaultFetcher is the CredentialFetcher backed by Azure Key Vault,
// authenticated via Azure AD Workload Identity — no static Azure credential
// anywhere in this process (docs/nats-auth-middleware/design.md §8;
// docs/nats-tenant-queue-api/design.md invariant on no static credentials).
type KeyVaultFetcher struct {
	client *azsecrets.Client
}

// NewKeyVaultFetcher builds a fetcher against the Key Vault at vaultURL
// using the given credential (typically azidentity.NewWorkloadIdentityCredential
// or azidentity.NewDefaultAzureCredential, which resolves to Workload
// Identity automatically inside AKS).
func NewKeyVaultFetcher(vaultURL string, credential azcore.TokenCredential) (*KeyVaultFetcher, error) {
	client, err := azsecrets.NewClient(vaultURL, credential, nil)
	if err != nil {
		return nil, fmt.Errorf("pool: create key vault client: %w", err)
	}
	return &KeyVaultFetcher{client: client}, nil
}

// Fetch reads a tenant's NATS user JWT and seed from Key Vault, named
// tenant-<tenant_id>-nats-jwt / tenant-<tenant_id>-nats-seed
// (docs/nats-auth-middleware/design.md §6, Naming and identity;
// docs/tenant-provisioning/design.md §6). Returns ErrTenantNotProvisioned if
// either secret is missing — a tenant with no matching entry is inert by
// design, never a fallback (§7).
func (f *KeyVaultFetcher) Fetch(ctx context.Context, tenantID string) (jwt, seed string, err error) {
	jwt, err = f.getSecret(ctx, "tenant-"+tenantID+"-nats-jwt")
	if err != nil {
		return "", "", err
	}
	seed, err = f.getSecret(ctx, "tenant-"+tenantID+"-nats-seed")
	if err != nil {
		return "", "", err
	}
	return jwt, seed, nil
}

func (f *KeyVaultFetcher) getSecret(ctx context.Context, name string) (string, error) {
	resp, err := f.client.GetSecret(ctx, name, "", nil)
	if err != nil {
		var respErr *azcore.ResponseError
		if errors.As(err, &respErr) && respErr.StatusCode == http.StatusNotFound {
			return "", ErrTenantNotProvisioned
		}
		return "", fmt.Errorf("pool: fetch secret %q: %w", name, err)
	}
	if resp.Value == nil {
		return "", ErrTenantNotProvisioned
	}
	return *resp.Value, nil
}
