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

package utils

import (
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/kserve/kserve/pkg/constants"
)

func TestAuditLoggingContext(t *testing.T) {
	tests := []struct {
		name        string
		store       bool
		profile     constants.AuditLoggingProfile
		manage      bool
		wantProfile constants.AuditLoggingProfile
		wantManage  bool
	}{
		{
			name:        "no stored settings defaults to unmanaged none",
			wantProfile: constants.AuditLoggingProfileNone,
		},
		{
			name:        "managed metadata profile round-trips",
			store:       true,
			profile:     constants.AuditLoggingProfileMetadata,
			manage:      true,
			wantProfile: constants.AuditLoggingProfileMetadata,
			wantManage:  true,
		},
		{
			name:        "managed none profile round-trips",
			store:       true,
			profile:     constants.AuditLoggingProfileNone,
			manage:      true,
			wantProfile: constants.AuditLoggingProfileNone,
			wantManage:  true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ctx := t.Context()
			if tt.store {
				ctx = WithAuditLogging(ctx, tt.profile, tt.manage)
			}

			profile, manage := AuditLoggingFromContext(ctx)

			assert.Equal(t, tt.wantProfile, profile)
			assert.Equal(t, tt.wantManage, manage)
		})
	}
}
