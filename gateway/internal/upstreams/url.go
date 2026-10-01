package upstreams

import (
	"fmt"
	"net/url"
)

// ValidateUpstreamURL valida uma URL de upstream (quick 260930-uru).
//
// Mesma regra que os constructors de proxy (NewEmbeddingsProxy, NewAudioProxy,
// NewTTSProxy, NewRerankProxy) aplicam à URL vinda da env: parse sem erro,
// scheme http ou https e host não vazio. É compartilhada entre o loader (que
// rejeita url_override inválido e cai na env) e o gatewayctl (que recusa
// gravar um override inválido), para que o banco nunca carregue um valor que
// o gateway não conseguiria usar.
func ValidateUpstreamURL(s string) error {
	if s == "" {
		return fmt.Errorf("upstream url is empty")
	}
	u, err := url.Parse(s)
	if err != nil {
		return fmt.Errorf("upstream url %q: %w", s, err)
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return fmt.Errorf("upstream url %q: scheme must be http or https", s)
	}
	if u.Host == "" {
		return fmt.Errorf("upstream url %q: host is empty", s)
	}
	return nil
}
