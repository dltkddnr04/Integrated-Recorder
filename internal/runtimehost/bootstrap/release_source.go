package bootstrap

import (
	"errors"

	"github.com/dltkddnr04/integrated-recorder/internal/buildinfo"
	"github.com/dltkddnr04/integrated-recorder/internal/runtimehost/install"
	"github.com/dltkddnr04/integrated-recorder/internal/runtimehost/source"
)

func configuredReleaseSourceFactory(config Config, applicationBuild buildinfo.Info) func() (install.Source, error) {
	if config.ReleaseBundleDir != "" {
		return func() (install.Source, error) {
			return install.LocalDirectorySource{Directory: config.ReleaseBundleDir}, nil
		}
	}
	channel := source.Stable
	switch applicationBuild.ReleaseChannel {
	case "stable":
		channel = source.Stable
	case "prerelease":
		channel = source.Prerelease
	default:
		return func() (install.Source, error) {
			return nil, errors.New("development builds do not have a remote release channel")
		}
	}
	return func() (install.Source, error) {
		return source.NewGitHubReleaseSource(channel)
	}
}
