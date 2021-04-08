package adapter

import (
	"context"
	"net/http"

	"github.com/epmd-edp/keycloak-operator/pkg/client/keycloak/dto"
	"github.com/pkg/errors"
)

func (a GoCloakAdapter) ExistRealm(realmName string) (bool, error) {
	reqLog := log.WithValues("realm", realmName)
	reqLog.Info("Start check existing realm...")

	_, err := a.client.GetRealm(context.Background(), a.token.AccessToken, realmName)

	res, err := strip404(err)
	if err != nil {
		return false, err
	}

	reqLog.Info("Check existing realm has been finished", "result", res)
	return res, nil
}

func (a GoCloakAdapter) CreateRealmWithDefaultConfig(realm *dto.Realm) error {
	reqLog := log.WithValues("realm", realm)
	reqLog.Info("Start creating realm with default config...")

	_, err := a.client.CreateRealm(context.Background(), a.token.AccessToken, getDefaultRealm(realm))
	if err != nil {
		return err
	}

	reqLog.Info("End creating realm with default config")
	return nil
}

func (a GoCloakAdapter) DeleteRealm(realmName string) error {
	reqLog := log.WithValues("realm", realmName)
	reqLog.Info("Start deleting realm...")

	if err := a.client.DeleteRealm(context.Background(), a.token.AccessToken, realmName); err != nil {
		return err
	}

	reqLog.Info("End deletion realm")
	return nil
}

func (a GoCloakAdapter) SyncRealmIdentityProviderMappers(realmName string, mappers []dto.IdentityProviderMapper) error {
	realm, err := a.client.GetRealm(context.Background(), a.token.AccessToken, realmName)
	if err != nil {
		return errors.Wrapf(err, "unable to get realm by name: %s", realmName)
	}

	currentMappers := make(map[string]*dto.IdentityProviderMapper)

	if realm.IdentityProviderMappers != nil {
		for _, idpm := range *realm.IdentityProviderMappers {
			if idpmTyped, ok := decodeIdentityProviderMapper(idpm); ok {
				currentMappers[idpmTyped.Name] = idpmTyped
			}
		}
	}

	for _, claimedMapper := range mappers {
		if idpmTyped, ok := currentMappers[claimedMapper.Name]; ok {
			claimedMapper.ID = idpmTyped.ID
			if err := a.updateIdentityProviderMapper(realmName, claimedMapper); err != nil {
				return errors.Wrapf(err, "unable to update idp mapper: %+v", claimedMapper)
			}

			continue
		}

		if err := a.createIdentityProviderMapper(realmName, claimedMapper); err != nil {
			return errors.Wrapf(err, "unable to create idp mapper: %+v", claimedMapper)
		}
	}

	return nil
}

func decodeIdentityProviderMapper(mp interface{}) (*dto.IdentityProviderMapper, bool) {
	mapInterface, ok := mp.(map[string]interface{})
	if !ok {
		return nil, false
	}

	mapper := dto.IdentityProviderMapper{
		Config: make(map[string]string),
	}

	if idRaw, ok := mapInterface["id"]; ok {
		if id, ok := idRaw.(string); ok {
			mapper.ID = id
		}
	}

	if nameRaw, ok := mapInterface["name"]; ok {
		if name, ok := nameRaw.(string); ok {
			mapper.Name = name
		}
	}

	if identityProviderAliasRaw, ok := mapInterface["identityProviderAlias"]; ok {
		if identityProviderAlias, ok := identityProviderAliasRaw.(string); ok {
			mapper.IdentityProviderAlias = identityProviderAlias
		}
	}

	if identityProviderMapperRaw, ok := mapInterface["identityProviderMapper"]; ok {
		if identityProviderMapper, ok := identityProviderMapperRaw.(string); ok {
			mapper.IdentityProviderMapper = identityProviderMapper
		}
	}

	if configRaw, ok := mapInterface["config"]; ok {
		if configInterface, ok := configRaw.(map[string]interface{}); ok {
			for k, v := range configInterface {
				if value, ok := v.(string); ok {
					mapper.Config[k] = value
				}
			}
		}
	}

	return &mapper, true
}

func (a GoCloakAdapter) createIdentityProviderMapper(realmName string, mapper dto.IdentityProviderMapper) error {
	resp, err := a.startRestyRequest().SetPathParams(map[string]string{
		"alias": mapper.IdentityProviderAlias,
		"realm": realmName,
	}).SetBody(mapper).Post(a.basePath + mapperToIdentityProvider)

	if err != nil {
		return errors.Wrapf(err, "unable to create identity provider mapper: %+v", mapper)
	}

	if resp.StatusCode() != http.StatusCreated {
		return errors.Errorf("unable to create identity provider mapper: %+v, response: %s", mapper,
			resp.String())
	}

	return nil
}

func (a GoCloakAdapter) updateIdentityProviderMapper(realmName string, mapper dto.IdentityProviderMapper) error {
	resp, err := a.startRestyRequest().SetPathParams(map[string]string{
		"alias": mapper.IdentityProviderAlias,
		"realm": realmName,
		"id":    mapper.ID,
	}).SetBody(mapper).Put(a.basePath + updateMapperToIdentityProvider)

	if err != nil {
		return errors.Wrapf(err, "unable to update identity provider mapper: %+v", mapper)
	}

	if resp.StatusCode() != http.StatusOK {
		return errors.Errorf("unable to update identity provider mapper: %+v, response: %s", mapper,
			resp.String())
	}

	return nil
}
