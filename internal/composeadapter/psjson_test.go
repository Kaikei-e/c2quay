package composeadapter_test

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/Kaikei-e/c2quay/internal/composeadapter"
)

func TestParsePsJSON_Array(t *testing.T) {
	raw := []byte(`[{"Name":"app-api-1","Service":"api","State":"running","Health":"healthy"}]`)
	s, err := composeadapter.ParsePsJSON(raw)
	require.NoError(t, err)
	require.Len(t, s, 1)
	assert.Equal(t, "api", s[0].Service)
	assert.Equal(t, "running", s[0].State)
}

func TestParsePsJSON_NDJSON(t *testing.T) {
	raw := []byte(`{"Name":"a","Service":"api","State":"running","Health":"healthy"}
{"Name":"b","Service":"init","State":"exited","ExitCode":0}`)
	s, err := composeadapter.ParsePsJSON(raw)
	require.NoError(t, err)
	require.Len(t, s, 2)
	assert.Equal(t, "init", s[1].Service)
	assert.Equal(t, 0, s[1].ExitCode)
}

func TestAllServicesHealthy(t *testing.T) {
	cases := []struct {
		name string
		in   []composeadapter.ContainerStatus
		want bool
	}{
		{"empty", nil, false},
		{"running healthy", []composeadapter.ContainerStatus{{State: "running", Health: "healthy"}}, true},
		{"running none", []composeadapter.ContainerStatus{{State: "running", Health: "none"}}, true},
		{"running no health", []composeadapter.ContainerStatus{{State: "running"}}, true},
		{"running unhealthy", []composeadapter.ContainerStatus{{State: "running", Health: "unhealthy"}}, false},
		{"running starting", []composeadapter.ContainerStatus{{State: "running", Health: "starting"}}, false},
		{"exited 0", []composeadapter.ContainerStatus{{State: "exited", ExitCode: 0}}, true},
		{"exited 1", []composeadapter.ContainerStatus{{State: "exited", ExitCode: 1}}, false},
		{"mixed ok", []composeadapter.ContainerStatus{
			{State: "running", Health: "healthy"},
			{State: "exited", ExitCode: 0},
		}, true},
		{"one bad", []composeadapter.ContainerStatus{
			{State: "running", Health: "healthy"},
			{State: "exited", ExitCode: 2},
		}, false},
	}
	for _, tc := range cases {
		assert.Equal(t, tc.want, composeadapter.AllServicesHealthy(tc.in), tc.name)
	}
}
