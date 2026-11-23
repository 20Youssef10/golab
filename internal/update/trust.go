package update

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"time"

	"github.com/sigstore/sigstore-go/pkg/root"
	"github.com/sigstore/sigstore-go/pkg/tuf"
	"github.com/theupdateframework/go-tuf/v2/metadata/fetcher"
)

// trustedRootBudget bounds the TUF refresh used to obtain the Sigstore public
// good trusted root. No network operation in this package is unbounded.
const trustedRootBudget = 15 * time.Second

// trustedMaterialLoader is the single seam through which verification obtains
// its trust anchors. Production always loads the public good instance's TUF
// trusted root; tests replace it with a virtual Sigstore so they never hit the
// network. It is a variable, not a Config field, so the exported Config shape
// stays stable and callers cannot weaken verification by supplying their own
// trust anchors.
var trustedMaterialLoader = loadPublicGoodTrustedMaterial

// loadPublicGoodTrustedMaterial fetches the current Fulcio/Rekor/CT/TSA trust
// anchors from Sigstore's TUF repository. TUF (not a vendored snapshot) is
// required because Fulcio roots and Rekor keys rotate; a stale pinned root
// would either reject legitimate releases or, worse, be tempting to bypass.
// A failure here is fatal for the update: there is no weaker fallback path.
func loadPublicGoodTrustedMaterial(ctx context.Context) (root.TrustedMaterial, error) {
	ctx, cancel := context.WithTimeout(ctx, trustedRootBudget)
	defer cancel()

	opts := tuf.DefaultOptions().WithContext(ctx)
	opts.CacheValidity = 1 // refresh TUF at most once per day
	client := &http.Client{
		Timeout: trustedRootBudget,
		Transport: &http.Transport{
			Proxy:                 http.ProxyFromEnvironment,
			DialContext:           (&net.Dialer{Timeout: connectTimeout, KeepAlive: idleTimeout}).DialContext,
			TLSHandshakeTimeout:   tlsTimeout,
			ResponseHeaderTimeout: headerTimeout,
			IdleConnTimeout:       idleTimeout,
			MaxIdleConns:          4,
		},
	}
	f := fetcher.NewDefaultFetcher()
	f.SetHTTPClient(client)
	f.SetHTTPUserAgent("golab-self-update")
	opts.Fetcher = f

	tufClient, err := tuf.New(opts)
	if err != nil {
		return nil, fmt.Errorf("%w: initialise TUF client: %w", ErrTrustedRoot, err)
	}
	trustedRoot, err := root.GetTrustedRoot(tufClient)
	if err != nil {
		return nil, fmt.Errorf("%w: fetch Sigstore trusted root: %w", ErrTrustedRoot, err)
	}
	return root.TrustedMaterialCollection{trustedRoot}, nil
}
