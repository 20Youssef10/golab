// Portions derived from google-colab-cli, © Google LLC, Apache-2.0; modified.
package auth

// Individual OAuth scopes, matching upstream PUBLIC_SCOPES in auth.py.
const (
	OpenIDScope        = "openid"
	ProfileScope       = "https://www.googleapis.com/auth/userinfo.profile"
	EmailScope         = "https://www.googleapis.com/auth/userinfo.email"
	CloudPlatformScope = "https://www.googleapis.com/auth/cloud-platform"
	CollaboratoryScope = "https://www.googleapis.com/auth/colaboratory"
	DriveFileScope     = "https://www.googleapis.com/auth/drive.file"
)

// Scopes is the six-scope set requested by upstream by default. Treat it as
// read-only; copy it before modifying (Provider.HTTPClient already does).
var Scopes = []string{
	OpenIDScope,
	ProfileScope,
	EmailScope,
	CloudPlatformScope,
	CollaboratoryScope,
	DriveFileScope,
}

// MinimalScopes is the candidate reduced scope set from DEC-10 / doc 08 §5
// (spike S5): it drops cloud-platform (very broad), drive.file (only needed
// for Drive mount) and userinfo.profile. It is an EXPERIMENT, not the default;
// Scopes remains the compatibility choice. gcloud requires openid,
// cloud-platform, userinfo.email and colaboratory for ADC user credentials,
// so this set is only meaningful for the OAuth2 provider. Keeping
// colaboratory is mandatory: without it /tun/m/* returns 401/403.
var MinimalScopes = []string{
	OpenIDScope,
	EmailScope,
	CollaboratoryScope,
}
