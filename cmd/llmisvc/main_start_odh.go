//go:build distro

/*
Copyright 2026 The KServe Authors.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package main

import (
	"context"
	"crypto/tls"

	"k8s.io/client-go/rest"
	ctrl "sigs.k8s.io/controller-runtime"

	distrotls "github.com/kserve/kserve/pkg/tls/distro"
)

var lastTLSResult distrotls.Result

func resolveTLS(ctx context.Context, cfg *rest.Config, minVer, ciphers string) ([]func(*tls.Config), error) {
	res, err := distrotls.Resolve(ctx, cfg, minVer, ciphers)
	if err != nil {
		return nil, err
	}
	lastTLSResult = res
	return res.TLSOpts, nil
}

func setupDistroStartup(ctx context.Context, mgr ctrl.Manager) (context.Context, error) {
	childCtx, cancel := context.WithCancel(ctx)
	err := distrotls.SetupProfileWatcher(mgr, lastTLSResult, func(_ context.Context, old, cur distrotls.Settings) {
		setupLog.Info("TLS settings changed, shutting down for restart",
			"oldMinTLS", old.ProfileSpec.MinTLSVersion,
			"newMinTLS", cur.ProfileSpec.MinTLSVersion,
			"oldAdherence", string(old.Adherence),
			"newAdherence", string(cur.Adherence))
		cancel()
	})
	if err != nil {
		cancel()
		return ctx, err
	}
	return childCtx, nil
}
