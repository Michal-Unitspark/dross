package ship

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"
)

// adoAnonymousID is the identity Azure DevOps reports for a caller it did not
// authenticate. It is nobody, and never dross.
const adoAnonymousID = "aaaaaaaa-aaaa-aaaa-aaaa-aaaaaaaaaaaa"

// withPreview is the connection at the preview of its api-version. A few
// endpoints — connectionData, policy evaluations — exist only as previews and
// refuse a released version: 7.1 becomes 7.1-preview.1, 6.0 becomes
// 6.0-preview.1, and a version that is already a preview is kept as it is.
func (c adoConn) withPreview() adoConn {
	if !strings.Contains(c.version, "-preview") {
		c.version += "-preview.1"
	}
	return c
}

// azureDevOpsAuthenticatedUser reads the identity the credential acts as from
// connectionData at the org/collection base — no project segment. Its id is
// lowercased exactly as a thread author's is, so a thread dross posted reads as
// dross's own.
func azureDevOpsAuthenticatedUser(opts OpenOpts) (Account, error) {
	c, err := adoOpenConn(opts)
	if err != nil {
		return Account{}, err
	}
	rb, _, err := adoRequest(c.withPreview(), "GET", c.apiBase+"/_apis/connectionData", nil)
	if err != nil {
		return Account{}, fmt.Errorf("read the authenticated account: %w", err)
	}
	var data struct {
		AuthenticatedUser *struct {
			ID         string `json:"id"`
			Properties struct {
				Account *struct {
					Value string `json:"$value"`
				} `json:"Account"`
			} `json:"properties"`
		} `json:"authenticatedUser"`
	}
	if err := json.Unmarshal(rb, &data); err != nil {
		return Account{}, errors.New("read the authenticated account: the response is not the JSON it promises")
	}
	u := data.AuthenticatedUser
	if u == nil || u.ID == "" || strings.EqualFold(u.ID, adoAnonymousID) {
		return Account{}, errors.New("read the authenticated account: Azure DevOps names no authenticated identity — check the token's scopes")
	}
	// The unique name, never providerDisplayName: a display name is not unique.
	if u.Properties.Account == nil || u.Properties.Account.Value == "" {
		return Account{}, errors.New("read the authenticated account: Azure DevOps' answer carries no account name")
	}
	return Account{ID: strings.ToLower(u.ID), Login: u.Properties.Account.Value}, nil
}
