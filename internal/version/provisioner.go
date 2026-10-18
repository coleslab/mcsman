package version

import (
	"context"
	"errors"
)

// Provisioner implements server.Provisioner on top of the Fill API.
type Provisioner struct {
	Client *Client
	Jars   *JarStore
}

func (p *Provisioner) JavaFor(ctx context.Context, mc string) (int, bool, error) {
	info, err := p.Client.Info(ctx, mc)
	if errors.Is(err, ErrUnknownVersion) {
		return 0, false, nil
	}
	if err != nil {
		return 0, false, err
	}
	return info.JavaMinimum, true, nil
}

func (p *Provisioner) InstallJar(ctx context.Context, mc, dir string) error {
	_, err := p.Jars.InstallLatest(ctx, mc, dir)
	return err
}
