package config

type environmentCredential struct {
	runtime, saved string
}

const legacyCredential = "legacy"

func (c *APIConfig) environmentKey(provider, value string) {
	previous := c.APIKey
	if p := GetProvider(provider); p != nil {
		previous = p.GetKey(c)
	}
	if c.environmentCredentials == nil {
		c.environmentCredentials = make(map[string]environmentCredential)
	}
	if prior, ok := c.environmentCredentials[provider]; ok && previous == prior.runtime {
		previous = prior.saved
	}
	c.environmentCredentials[provider] = environmentCredential{runtime: value, saved: previous}
	if provider == legacyCredential {
		c.APIKey = value
	} else if p := GetProvider(provider); p != nil {
		p.SetKey(c, value)
	}
}

// MarshalYAML writes only credentials owned by the saved configuration. The
// live API fields retain their environment values; an explicit /login uses
// SetProviderKey to promote that provider's chosen value to persisted config.
func (c APIConfig) MarshalYAML() (any, error) {
	type persistedAPI APIConfig
	for provider, origin := range c.environmentCredentials {
		if provider == legacyCredential {
			if c.APIKey == origin.runtime {
				c.APIKey = origin.saved
			}
		} else if p := GetProvider(provider); p != nil && p.GetKey(&c) == origin.runtime {
			p.SetKey(&c, origin.saved)
		}
	}
	return persistedAPI(c), nil
}
