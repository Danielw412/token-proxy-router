// Package backend starts Token Proxy Router's in-process features once the auth
// manager exists. Importing it links their routes into the server.
package backend

import (
	"context"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/tpr/alerts"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/tpr/health"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/tpr/home"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/tpr/keyusage"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/tpr/quota"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/tpr/router"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/tpr/store"
	coreauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
	log "github.com/sirupsen/logrus"
)

// Start brings the features up and returns the function that stops them. Nothing
// here can stop the server: a feature that cannot start logs why, its routes keep
// answering 503, and requests are routed by upstream selection or by whatever did
// start. The router needs no database, so it runs even if the database cannot open.
func Start(mgr *coreauth.Manager, cfg func() *config.Config) (stop func()) {
	stop = func() {}
	defer func() {
		if r := recover(); r != nil {
			log.Errorf("token proxy router features could not start: %v", r)
		}
	}()
	if mgr == nil {
		return stop
	}
	if err := home.Ensure(); err != nil {
		log.Warnf("could not create the data folder %s: %v", home.Dir(), err)
	}

	var stops []func()
	stop = func() {
		for i := len(stops) - 1; i >= 0; i-- {
			stops[i]()
		}
	}
	r := router.Start(mgr, cfg)
	stops = append(stops, r.Stop)

	st, err := store.OpenDefault()
	if err != nil {
		log.Errorf("quota history, alerts and usage per key are off: %v", err)
		h := health.Start(mgr, nil)
		stops = append(stops, h.Stop)
		return stop
	}
	stops = append(stops, func() {
		if errClose := st.Close(); errClose != nil {
			log.Warnf("could not close the database: %v", errClose)
		}
	})
	ctx, cancel := context.WithCancel(context.Background())
	go st.RunRetention(ctx)
	stops = append(stops, cancel)

	q := quota.Start(st, r)
	stops = append(stops, q.Stop)
	a := alerts.Start(st, q)
	stops = append(stops, a.Stop)
	k := keyusage.Start(st, cfg)
	stops = append(stops, k.Stop)
	h := health.Start(mgr, st)
	stops = append(stops, h.Stop)
	return stop
}
