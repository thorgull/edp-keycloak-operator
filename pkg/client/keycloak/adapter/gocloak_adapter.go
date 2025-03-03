package adapter

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"reflect"
	"strconv"
	"strings"
	"time"

	"github.com/Nerzal/gocloak/v12"
	"github.com/go-logr/logr"
	"github.com/go-resty/resty/v2"
	"github.com/pkg/errors"

	"github.com/epam/edp-keycloak-operator/pkg/client/keycloak/api"
	"github.com/epam/edp-keycloak-operator/pkg/client/keycloak/dto"
)

const (
	idPResource                     = "/admin/realms/{realm}/identity-provider/instances"
	idPMapperResource               = "/admin/realms/{realm}/identity-provider/instances/{alias}/mappers"
	getOneIdP                       = idPResource + "/{alias}"
	openIdConfig                    = "/realms/{realm}/.well-known/openid-configuration"
	authExecutions                  = "/admin/realms/{realm}/authentication/flows/browser/executions"
	authExecutionConfig             = "/admin/realms/{realm}/authentication/executions/{id}/config"
	postClientScopeMapper           = "/admin/realms/{realm}/client-scopes/{scopeId}/protocol-mappers/models"
	getRealmClientScopes            = "/admin/realms/{realm}/client-scopes"
	postClientScope                 = "/admin/realms/{realm}/client-scopes"
	putClientScope                  = "/admin/realms/{realm}/client-scopes/{id}"
	getClientProtocolMappers        = "/admin/realms/{realm}/clients/{id}/protocol-mappers/models"
	mapperToIdentityProvider        = "/admin/realms/{realm}/identity-provider/instances/{alias}/mappers"
	updateMapperToIdentityProvider  = "/admin/realms/{realm}/identity-provider/instances/{alias}/mappers/{id}"
	authFlows                       = "/admin/realms/{realm}/authentication/flows"
	authFlow                        = "/admin/realms/{realm}/authentication/flows/{id}"
	authFlowExecutionCreate         = "/admin/realms/{realm}/authentication/executions"
	authFlowExecutionGetUpdate      = "/admin/realms/{realm}/authentication/flows/{alias}/executions"
	authFlowExecutionDelete         = "/admin/realms/{realm}/authentication/executions/{id}"
	raiseExecutionPriority          = "/admin/realms/{realm}/authentication/executions/{id}/raise-priority"
	lowerExecutionPriority          = "/admin/realms/{realm}/authentication/executions/{id}/lower-priority"
	authFlowExecutionConfig         = "/admin/realms/{realm}/authentication/executions/{id}/config"
	authFlowConfig                  = "/admin/realms/{realm}/authentication/config/{id}"
	deleteClientScopeProtocolMapper = "/admin/realms/{realm}/client-scopes/{clientScopeID}/protocol-mappers/models/{protocolMapperID}"
	createClientScopeProtocolMapper = "/admin/realms/{realm}/client-scopes/{clientScopeID}/protocol-mappers/models"
	putDefaultClientScope           = "/admin/realms/{realm}/default-default-client-scopes/{clientScopeID}"
	deleteDefaultClientScope        = "/admin/realms/{realm}/default-default-client-scopes/{clientScopeID}"
	getDefaultClientScopes          = "/admin/realms/{realm}/default-default-client-scopes"
	realmEventConfigPut             = "/admin/realms/{realm}/events/config"
	realmComponent                  = "/admin/realms/{realm}/components"
	realmComponentEntity            = "/admin/realms/{realm}/components/{id}"
	identityProviderEntity          = "/admin/realms/{realm}/identity-provider/instances/{alias}"
	identityProviderCreateList      = "/admin/realms/{realm}/identity-provider/instances"
	idpMapperCreateList             = "/admin/realms/{realm}/identity-provider/instances/{alias}/mappers"
	idpMapperEntity                 = "/admin/realms/{realm}/identity-provider/instances/{alias}/mappers/{id}"
	deleteRealmUser                 = "/admin/realms/{realm}/users/{id}"
	setRealmUserPassword            = "/admin/realms/{realm}/users/{id}/reset-password"
	getUserRealmRoleMappings        = "/admin/realms/{realm}/users/{id}/role-mappings/realm"
	getUserGroupMappings            = "/admin/realms/{realm}/users/{id}/groups"
	manageUserGroups                = "/admin/realms/{realm}/users/{userID}/groups/{groupID}"
	logClientDTO                    = "client dto"
)

const (
	keycloakApiParamId            = "id"
	keycloakApiParamRole          = "role"
	keycloakApiParamRealm         = "realm"
	keycloakApiParamAlias         = "alias"
	keycloakApiParamClientScopeId = "clientScopeID"
)

const (
	logKeyUser  = "user dto"
	logKeyRealm = "realm"
)

type TokenExpiredError string

func (e TokenExpiredError) Error() string {
	return string(e)
}

func IsErrTokenExpired(err error) bool {
	errTokenExpired := TokenExpiredError("")

	return errors.As(err, &errTokenExpired)
}

type GoCloakAdapter struct {
	client     GoCloak
	token      *gocloak.JWT
	log        logr.Logger
	basePath   string
	legacyMode bool
}

type JWTPayload struct {
	Exp int64 `json:"exp"`
}

func (a GoCloakAdapter) GetGoCloak() GoCloak {
	return a.client
}

func MakeFromToken(url string, tokenData []byte, log logr.Logger) (*GoCloakAdapter, error) {
	var token gocloak.JWT
	if err := json.Unmarshal(tokenData, &token); err != nil {
		return nil, errors.Wrapf(err, "unable decode json data")
	}

	const requiredTokenParts = 3

	tokenParts := strings.Split(token.AccessToken, ".")

	if len(tokenParts) < requiredTokenParts {
		return nil, errors.New("wrong JWT token structure")
	}

	tokenPayload, err := base64.RawURLEncoding.DecodeString(tokenParts[1])
	if err != nil {
		return nil, errors.Wrap(err, "wrong JWT token base64 encoding")
	}

	var tokenPayloadDecoded JWTPayload
	if err = json.Unmarshal(tokenPayload, &tokenPayloadDecoded); err != nil {
		return nil, errors.Wrap(err, "unable to decode JWT payload json")
	}

	if tokenPayloadDecoded.Exp < time.Now().Unix() {
		return nil, TokenExpiredError("token is expired")
	}

	kcCl, legacyMode, err := makeClientFromToken(url, token.AccessToken)
	if err != nil {
		return nil, fmt.Errorf("failed to make new keycloak client: %w", err)
	}

	return &GoCloakAdapter{
		client:     kcCl,
		token:      &token,
		log:        log,
		basePath:   url,
		legacyMode: legacyMode,
	}, nil
}

// makeClientFromToken returns Keycloak client, a bool flag indicating whether it was created in legacy mode and an error.
func makeClientFromToken(url, token string) (*gocloak.GoCloak, bool, error) {
	restyClient := resty.New()

	kcCl := gocloak.NewClient(url)
	kcCl.SetRestyClient(restyClient)

	_, err := kcCl.GetRealms(context.Background(), token)
	if err == nil {
		return kcCl, false, nil
	}

	if !strings.Contains(err.Error(), "404 Not Found") {
		return nil, false, fmt.Errorf("unexpected error received while trying to get realms using the modern client: %w", err)
	}

	kcCl = gocloak.NewClient(url, gocloak.SetLegacyWildFlySupport())
	kcCl.SetRestyClient(restyClient)

	if _, err := kcCl.GetRealms(context.Background(), token); err != nil {
		return nil, false, fmt.Errorf("failed to create both current and legacy clients: %w", err)
	}

	return kcCl, true, nil
}

func MakeFromServiceAccount(ctx context.Context,
	url, clientID, clientSecret, realm string,
	log logr.Logger, restyClient *resty.Client,
) (*GoCloakAdapter, error) {
	if restyClient == nil {
		restyClient = resty.New()
	}

	kcCl := gocloak.NewClient(url)
	kcCl.SetRestyClient(restyClient)

	token, err := kcCl.LoginClient(ctx, clientID, clientSecret, realm)
	if err == nil {
		return &GoCloakAdapter{
			client:     kcCl,
			token:      token,
			log:        log,
			basePath:   url,
			legacyMode: false,
		}, nil
	}

	if !strings.Contains(err.Error(), "404 Not Found") {
		return nil, fmt.Errorf("unexpected error received while trying to get realms using the modern client: %w", err)
	}

	kcCl = gocloak.NewClient(url, gocloak.SetLegacyWildFlySupport())
	kcCl.SetRestyClient(restyClient)

	token, err = kcCl.LoginClient(ctx, clientID, clientSecret, realm)
	if err != nil {
		return nil, fmt.Errorf("failed to login with client creds on both current and legacy clients - "+
			"clientID: %s, realm: %s: %w", clientID, realm, err)
	}

	return &GoCloakAdapter{
		client:     kcCl,
		token:      token,
		log:        log,
		basePath:   url,
		legacyMode: true,
	}, nil
}

func Make(ctx context.Context, url, user, password string, log logr.Logger, restyClient *resty.Client) (*GoCloakAdapter, error) {
	if restyClient == nil {
		restyClient = resty.New()
	}

	kcCl := gocloak.NewClient(url)
	kcCl.SetRestyClient(restyClient)

	token, err := kcCl.LoginAdmin(ctx, user, password, "master")
	if err == nil {
		return &GoCloakAdapter{
			client:     kcCl,
			token:      token,
			log:        log,
			basePath:   url,
			legacyMode: false,
		}, nil
	}

	if !strings.Contains(err.Error(), "404 Not Found") {
		return nil, fmt.Errorf("unexpected error received while trying to get realms using the modern client: %w", err)
	}

	kcCl = gocloak.NewClient(url, gocloak.SetLegacyWildFlySupport())
	kcCl.SetRestyClient(restyClient)

	token, err = kcCl.LoginAdmin(ctx, user, password, "master")
	if err != nil {
		return nil, errors.Wrapf(err, "cannot login to keycloak server with user: %s", user)
	}

	return &GoCloakAdapter{
		client:     kcCl,
		token:      token,
		log:        log,
		basePath:   url,
		legacyMode: true,
	}, nil
}

func (a GoCloakAdapter) ExportToken() ([]byte, error) {
	tokenData, err := json.Marshal(a.token)
	if err != nil {
		return nil, errors.Wrap(err, "unable to json encode token")
	}

	return tokenData, nil
}

// buildPath returns request path corresponding with the mode the client is operating in.
func (a GoCloakAdapter) buildPath(endpoint string) string {
	if a.legacyMode {
		return a.basePath + "/auth" + endpoint
	}

	return a.basePath + endpoint
}

func (a GoCloakAdapter) ExistCentralIdentityProvider(realm *dto.Realm) (bool, error) {
	log := a.log.WithValues(logKeyRealm, realm)
	log.Info("Start check central identity provider in realm")

	resp, err := a.client.RestyClient().R().
		SetAuthToken(a.token.AccessToken).
		SetHeader("Content-Type", "application/json").
		SetPathParams(map[string]string{
			keycloakApiParamRealm: realm.Name,
			keycloakApiParamAlias: realm.SsoRealmName,
		}).
		Get(a.buildPath(getOneIdP))
	if err != nil {
		return false, fmt.Errorf("request exists central identity provider failed: %w", err)
	}

	if resp.StatusCode() == http.StatusNotFound {
		return false, nil
	}

	if resp.StatusCode() != http.StatusOK {
		return false, errors.Errorf("errors in get idP, response: %s", resp.String())
	}

	log.Info("End check central identity provider in realm")

	return true, nil
}

func (a GoCloakAdapter) CreateCentralIdentityProvider(realm *dto.Realm, client *dto.Client) error {
	log := a.log.WithValues(logKeyRealm, realm, "keycloak client", client)
	log.Info("Start create central identity provider...")

	idP := a.getCentralIdP(client, realm.SsoRealmName)

	resp, err := a.client.RestyClient().R().
		SetAuthToken(a.token.AccessToken).
		SetHeader(contentTypeHeader, contentTypeJson).
		SetPathParams(map[string]string{
			keycloakApiParamRealm: realm.Name,
		}).
		SetBody(idP).
		Post(a.buildPath(idPResource))

	if err != nil {
		return errors.Wrap(err, "unable to create central idp")
	}

	if resp.StatusCode() != http.StatusCreated {
		log.Info("requested url", "url", resp.Request.URL)
		return fmt.Errorf("error in create IdP, responce status: %s", resp.Status())
	}

	if !realm.DisableCentralIDPMappers {
		if err = a.CreateCentralIdPMappers(realm, client); err != nil {
			return errors.Wrap(err, "unable to create central idp mappers")
		}
	}

	log.Info("End create central identity provider")

	return nil
}

func (a GoCloakAdapter) getCentralIdP(client *dto.Client, ssoRealmName string) api.IdentityProviderRepresentation {
	return api.IdentityProviderRepresentation{
		Alias:       ssoRealmName,
		DisplayName: "EDP SSO",
		Enabled:     true,
		ProviderId:  "keycloak-oidc",
		Config: api.IdentityProviderConfig{
			UserInfoUrl:      a.buildPath(fmt.Sprintf("/realms/%s/protocol/openid-connect/userinfo", ssoRealmName)),
			TokenUrl:         a.buildPath(fmt.Sprintf("/realms/%s/protocol/openid-connect/token", ssoRealmName)),
			JwksUrl:          a.buildPath(fmt.Sprintf("/realms/%s/protocol/openid-connect/certs", ssoRealmName)),
			Issuer:           a.buildPath(fmt.Sprintf("/realms/%s", ssoRealmName)),
			AuthorizationUrl: a.buildPath(fmt.Sprintf("/realms/%s/protocol/openid-connect/auth", ssoRealmName)),
			LogoutUrl:        a.buildPath(fmt.Sprintf("/realms/%s/protocol/openid-connect/logout", ssoRealmName)),
			ClientId:         client.ClientId,
			ClientSecret:     client.ClientSecret,
		},
	}
}

func (a GoCloakAdapter) CreateCentralIdPMappers(realm *dto.Realm, client *dto.Client) error {
	log := a.log.WithValues(logKeyRealm, realm)
	log.Info("Start create central IdP mappers...")

	err := a.createIdPMapper(realm, client.ClientId+".administrator", "administrator")
	if err != nil {
		return errors.Wrap(err, "unable to create central idp mapper")
	}

	err = a.createIdPMapper(realm, client.ClientId+".developer", "developer")
	if err != nil {
		return errors.Wrap(err, "unable to create central idp mapper")
	}

	err = a.createIdPMapper(realm, client.ClientId+".administrator", "realm-management.realm-admin")
	if err != nil {
		return errors.Wrap(err, "unable to create central idp mapper")
	}

	log.Info("End create central IdP mappers")

	return nil
}

func (a GoCloakAdapter) createIdPMapper(realm *dto.Realm, externalRole string, role string) error {
	body := getIdPMapper(externalRole, role, realm.SsoRealmName)

	resp, err := a.client.RestyClient().R().
		SetAuthToken(a.token.AccessToken).
		SetHeader(contentTypeHeader, contentTypeJson).
		SetPathParams(map[string]string{
			keycloakApiParamRealm: realm.Name,
			keycloakApiParamAlias: realm.SsoRealmName,
		}).
		SetBody(body).
		Post(a.buildPath(idPMapperResource))
	if err != nil {
		return fmt.Errorf("request create idp mapper failed: %w", err)
	}

	if resp.StatusCode() != http.StatusCreated {
		return fmt.Errorf("error in creation idP mapper by name %s", body.Name)
	}

	return nil
}

func (a GoCloakAdapter) ExistClient(clientID, realm string) (bool, error) {
	log := a.log.WithValues("clientID", clientID, logKeyRealm, realm)
	log.Info("Start check client in Keycloak...")

	clns, err := a.client.GetClients(context.Background(), a.token.AccessToken, realm, gocloak.GetClientsParams{
		ClientID: &clientID,
	})

	if err != nil {
		return false, fmt.Errorf("failed to get clients for realm %s: %w", realm, err)
	}

	res := checkFullNameMatch(clientID, clns)

	log.Info("End check client in Keycloak")

	return res, nil
}

func (a GoCloakAdapter) ExistClientRole(client *dto.Client, clientRole string) (bool, error) {
	log := a.log.WithValues(logClientDTO, client, "client role", clientRole)
	log.Info("Start check client role in Keycloak...")

	id, err := a.GetClientID(client.ClientId, client.RealmName)
	if err != nil {
		return false, err
	}

	clientRoles, err := a.client.GetClientRoles(context.Background(), a.token.AccessToken, client.RealmName, id, gocloak.GetRoleParams{})

	_, err = strip404(err)
	if err != nil {
		return false, err
	}

	clientRoleExists := false

	for _, cl := range clientRoles {
		if cl.Name != nil && *cl.Name == clientRole {
			clientRoleExists = true
			break
		}
	}

	log.Info("End check client role in Keycloak", "clientRoleExists", clientRoleExists)

	return clientRoleExists, nil
}

func (a GoCloakAdapter) CreateClientRole(client *dto.Client, clientRole string) error {
	log := a.log.WithValues(logClientDTO, client, "client role", clientRole)
	log.Info("Start create client role in Keycloak...")

	id, err := a.GetClientID(client.ClientId, client.RealmName)
	if err != nil {
		return err
	}

	if _, err = a.client.CreateClientRole(context.Background(), a.token.AccessToken, client.RealmName, id, gocloak.Role{
		Name:       &clientRole,
		ClientRole: gocloak.BoolP(true),
	}); err != nil {
		return errors.Wrap(err, "unable to create client role")
	}

	log.Info("Keycloak client role has been created")

	return nil
}

func checkFullRoleNameMatch(role string, roles *[]gocloak.Role) bool {
	if roles == nil {
		return false
	}

	for _, cl := range *roles {
		if cl.Name != nil && *cl.Name == role {
			return true
		}
	}

	return false
}

func checkFullUsernameMatch(userName string, users []*gocloak.User) (*gocloak.User, bool) {
	if users == nil {
		return nil, false
	}

	for _, el := range users {
		if el.Username != nil && *el.Username == userName {
			return el, true
		}
	}

	return nil, false
}

func checkFullNameMatch(clientID string, clients []*gocloak.Client) bool {
	if clients == nil {
		return false
	}

	for _, el := range clients {
		if el.ClientID != nil && *el.ClientID == clientID {
			return true
		}
	}

	return false
}

func (a GoCloakAdapter) DeleteClient(ctx context.Context, kcClientID, realmName string) error {
	log := a.log.WithValues("client id", kcClientID)
	log.Info("Start delete client in Keycloak...")

	if err := a.client.DeleteClient(ctx, a.token.AccessToken, realmName, kcClientID); err != nil {
		return errors.Wrap(err, "unable to delete client")
	}

	log.Info("Keycloak client has been deleted")

	return nil
}

func (a GoCloakAdapter) UpdateClient(ctx context.Context, client *dto.Client) error {
	log := a.log.WithValues(logClientDTO, client)
	log.Info("Start update client in Keycloak...")

	if err := a.client.UpdateClient(ctx, a.token.AccessToken, client.RealmName, getGclCln(client)); err != nil {
		return fmt.Errorf("unable to update keycloak client: %w", err)
	}

	log.Info("Keycloak client has been updated")

	return nil
}

func (a GoCloakAdapter) CreateClient(ctx context.Context, client *dto.Client) error {
	log := a.log.WithValues(logClientDTO, client)
	log.Info("Start create client in Keycloak...")

	_, err := a.client.CreateClient(ctx, a.token.AccessToken, client.RealmName, getGclCln(client))
	if err != nil {
		return fmt.Errorf("failed to create keycloak client: %w", err)
	}

	log.Info("Keycloak client has been created")

	return nil
}

func getGclCln(client *dto.Client) gocloak.Client {
	//TODO: check collision with protocol mappers list in spec
	protocolMappers := getProtocolMappers(client.AdvancedProtocolMappers)

	cl := gocloak.Client{
		ClientID:                  &client.ClientId,
		Secret:                    &client.ClientSecret,
		PublicClient:              &client.Public,
		DirectAccessGrantsEnabled: &client.DirectAccess,
		RootURL:                   &client.WebUrl,
		Protocol:                  &client.Protocol,
		Attributes:                &client.Attributes,
		RedirectURIs: &[]string{
			client.WebUrl + "/*",
		},
		WebOrigins: &[]string{
			client.WebUrl,
		},
		AdminURL:               &client.WebUrl,
		ProtocolMappers:        &protocolMappers,
		ServiceAccountsEnabled: &client.ServiceAccountEnabled,
		FrontChannelLogout:     &client.FrontChannelLogout,
	}

	if client.ID != "" {
		cl.ID = &client.ID
	}

	return cl
}

func getProtocolMappers(need bool) []gocloak.ProtocolMapperRepresentation {
	if !need {
		return nil
	}

	return []gocloak.ProtocolMapperRepresentation{
		{
			Name:           gocloak.StringP("username"),
			Protocol:       gocloak.StringP("openid-connect"),
			ProtocolMapper: gocloak.StringP("oidc-usermodel-property-mapper"),
			Config: &map[string]string{
				"userinfo.token.claim": "true",
				"user.attribute":       "username",
				"id.token.claim":       "true",
				"access.token.claim":   "true",
				"claim.name":           "preferred_username",
				"jsonType.label":       "String",
			},
		},
		{
			Name:           gocloak.StringP("realm roles"),
			Protocol:       gocloak.StringP("openid-connect"),
			ProtocolMapper: gocloak.StringP("oidc-usermodel-realm-role-mapper"),
			Config: &map[string]string{
				"userinfo.token.claim": strconv.FormatBool(true),
				"multivalued":          strconv.FormatBool(true),
				"id.token.claim":       strconv.FormatBool(true),
				"access.token.claim":   strconv.FormatBool(false),
				"claim.name":           "roles",
				"jsonType.label":       "String",
			},
		},
	}
}

func (a GoCloakAdapter) GetClientID(clientID, realm string) (string, error) {
	clients, err := a.client.GetClients(context.Background(), a.token.AccessToken, realm,
		gocloak.GetClientsParams{
			ClientID: &clientID,
		})
	if err != nil {
		return "", errors.Wrap(err, "unable to get realm clients")
	}

	for _, item := range clients {
		if item.ClientID != nil && *item.ClientID == clientID {
			return *item.ID, nil
		}
	}

	return "", NotFoundError(fmt.Sprintf("unable to get Client ID. Client %v doesn't exist", clientID))
}

func getIdPMapper(externalRole, role, ssoRealmName string) api.IdentityProviderMapperRepresentation {
	return api.IdentityProviderMapperRepresentation{
		Config: map[string]string{
			"external.role":      externalRole,
			keycloakApiParamRole: role,
		},
		IdentityProviderAlias:  ssoRealmName,
		IdentityProviderMapper: "keycloak-oidc-role-to-role-idp-mapper",
		Name:                   role,
	}
}

func (a GoCloakAdapter) CreateRealmUser(realmName string, user *dto.User) error {
	log := a.log.WithValues(logKeyUser, user, logKeyRealm, realmName)
	log.Info("Start create realm user in Keycloak...")

	userDto := gocloak.User{
		Username: &user.Username,
		Email:    &user.Username,
		Enabled:  gocloak.BoolP(true),
	}

	_, err := a.client.CreateUser(context.Background(), a.token.AccessToken, realmName, userDto)
	if err != nil {
		return fmt.Errorf("failed to create user in realm %s: %w", realmName, err)
	}

	log.Info("Keycloak realm user has been created")

	return nil
}

func (a GoCloakAdapter) ExistRealmUser(realmName string, user *dto.User) (bool, error) {
	log := a.log.WithValues(logKeyUser, user, logKeyRealm, realmName)
	log.Info("Start check user in Keycloak realm...")

	usr, err := a.client.GetUsers(context.Background(), a.token.AccessToken, realmName, gocloak.GetUsersParams{
		Username: &user.Username,
	})

	_, err = strip404(err)
	if err != nil {
		return false, err
	}

	_, userExists := checkFullUsernameMatch(user.Username, usr)

	log.Info("End check user in Keycloak", "userExists", userExists)

	return userExists, nil
}

func (a GoCloakAdapter) DeleteRealmUser(ctx context.Context, realmName, username string) error {
	usrs, err := a.client.GetUsers(ctx, a.token.AccessToken, realmName, gocloak.GetUsersParams{
		Username: &username,
	})

	if err != nil {
		return errors.Wrap(err, "unable to get users")
	}

	usr, exists := checkFullUsernameMatch(username, usrs)
	if !exists {
		return NotFoundError("user not found")
	}

	rsp, err := a.startRestyRequest().
		SetPathParams(map[string]string{
			keycloakApiParamRealm: realmName,
			keycloakApiParamId:    *usr.ID,
		}).
		Delete(a.buildPath(deleteRealmUser))

	if err = a.checkError(err, rsp); err != nil {
		return errors.Wrap(err, "unable to delete user")
	}

	return nil
}

func (a GoCloakAdapter) HasUserRealmRole(realmName string, user *dto.User, role string) (bool, error) {
	log := a.log.WithValues(keycloakApiParamRole, role, logKeyRealm, realmName, logKeyUser, user)
	log.Info("Start check user roles in Keycloak realm...")

	users, err := a.client.GetUsers(context.Background(), a.token.AccessToken, realmName, gocloak.GetUsersParams{
		Username: &user.Username,
	})
	if err != nil {
		return false, errors.Wrap(err, "unable to get users from keycloak")
	}

	if len(users) == 0 {
		return false, fmt.Errorf("no such user %v has been found", user.Username)
	}

	rolesMapping, err := a.client.GetRoleMappingByUserID(context.Background(), a.token.AccessToken, realmName,
		*users[0].ID)
	if err != nil {
		return false, errors.Wrap(err, "unable to GetRoleMappingByUserID")
	}

	hasRealmRole := checkFullRoleNameMatch(role, rolesMapping.RealmMappings)

	log.Info("End check user role in Keycloak", "hasRealmRole", hasRealmRole)

	return hasRealmRole, nil
}

func (a GoCloakAdapter) HasUserClientRole(realmName string, clientId string, user *dto.User, role string) (bool, error) {
	log := a.log.WithValues(keycloakApiParamRole, role, "client", clientId, logKeyRealm, realmName, logKeyUser, user)
	log.Info("Start check user roles in Keycloak realm...")

	users, err := a.client.GetUsers(context.Background(), a.token.AccessToken, realmName, gocloak.GetUsersParams{
		Username: &user.Username,
	})
	if err != nil {
		return false, fmt.Errorf("failed to get user %s: %w", user.Username, err)
	}

	if len(users) == 0 {
		return false, errors.Errorf("no such user %v has been found", user.Username)
	}

	rolesMapping, err := a.client.GetRoleMappingByUserID(context.Background(), a.token.AccessToken, realmName,
		*users[0].ID)
	if err != nil {
		return false, fmt.Errorf("failed to get role mapping by user id %s: %w", *users[0].ID, err)
	}

	hasClientRole := false
	if clientMap, ok := rolesMapping.ClientMappings[clientId]; ok && clientMap != nil && clientMap.Mappings != nil {
		hasClientRole = checkFullRoleNameMatch(role, clientMap.Mappings)
	}

	log.Info("End check user role in Keycloak", "hasClientRole", hasClientRole)

	return hasClientRole, nil
}

func (a GoCloakAdapter) AddRealmRoleToUser(ctx context.Context, realmName, username, roleName string) error {
	users, err := a.client.GetUsers(ctx, a.token.AccessToken, realmName, gocloak.GetUsersParams{
		Username: &username,
	})
	if err != nil {
		return errors.Wrap(err, "error during get kc users")
	}

	if len(users) == 0 {
		return errors.Errorf("no users with username %s found", username)
	}

	rl, err := a.client.GetRealmRole(ctx, a.token.AccessToken, realmName, roleName)
	if err != nil {
		return errors.Wrap(err, "unable to get realm role from keycloak")
	}

	if err := a.client.AddRealmRoleToUser(ctx, a.token.AccessToken, realmName, *users[0].ID,
		[]gocloak.Role{
			*rl,
		}); err != nil {
		return errors.Wrap(err, "unable to add realm role to user")
	}

	return nil
}

func (a GoCloakAdapter) AddClientRoleToUser(realmName string, clientId string, user *dto.User, roleName string) error {
	log := a.log.WithValues(keycloakApiParamRole, roleName, logKeyRealm, realmName, "user", user.Username)
	log.Info("Start mapping realm role to user in Keycloak...")

	client, err := a.client.GetClients(context.Background(), a.token.AccessToken, realmName, gocloak.GetClientsParams{
		ClientID: &clientId,
	})
	if err != nil {
		return fmt.Errorf("failed to get client %s: %w", clientId, err)
	}

	if len(client) == 0 {
		return fmt.Errorf("no such client %v has been found", clientId)
	}

	role, err := a.client.GetClientRole(context.Background(), a.token.AccessToken, realmName, *client[0].ID, roleName)
	if err != nil {
		return errors.Wrap(err, "error during GetClientRole")
	}

	if role == nil {
		return errors.Errorf("no such client role %v has been found", roleName)
	}

	users, err := a.client.GetUsers(context.Background(), a.token.AccessToken, realmName, gocloak.GetUsersParams{
		Username: &user.Username,
	})
	if err != nil {
		return fmt.Errorf("failed to get user %s: %w", user.Username, err)
	}

	if len(users) == 0 {
		return fmt.Errorf("no such user %v has been found", user.Username)
	}

	err = a.addClientRoleToUser(realmName, *users[0].ID, []gocloak.Role{*role})
	if err != nil {
		return err
	}

	log.Info("Role to user has been added")

	return nil
}

func (a GoCloakAdapter) addClientRoleToUser(realmName string, userId string, roles []gocloak.Role) error {
	if err := a.client.AddClientRoleToUser(
		context.Background(),
		a.token.AccessToken, realmName,
		*roles[0].ContainerID,
		userId,
		roles,
	); err != nil {
		return fmt.Errorf("failed to add client role to user %s: %w", userId, err)
	}

	return nil
}

func getDefaultRealm(realm *dto.Realm) gocloak.RealmRepresentation {
	return gocloak.RealmRepresentation{
		Realm:   &realm.Name,
		Enabled: gocloak.BoolP(true),
		ID:      realm.ID,
	}
}

func strip404(in error) (bool, error) {
	if in == nil {
		return true, nil
	}

	if is404(in) {
		return false, nil
	}

	return false, in
}

func is404(e error) bool {
	return strings.Contains(e.Error(), "404")
}

func (a GoCloakAdapter) CreateIncludedRealmRole(realmName string, role *dto.IncludedRealmRole) error {
	log := a.log.WithValues(logKeyRealm, realmName, keycloakApiParamRole, role)
	log.Info("Start create realm roles in Keycloak...")

	realmRole := gocloak.Role{
		Name: &role.Name,
	}

	_, err := a.client.CreateRealmRole(context.Background(), a.token.AccessToken, realmName, realmRole)
	if err != nil {
		return fmt.Errorf("failed to create realm role %s: %w", role.Name, err)
	}

	persRole, err := a.client.GetRealmRole(context.Background(), a.token.AccessToken, realmName, role.Name)
	if err != nil {
		return fmt.Errorf("failed to get realm role %s: %w", role.Name, err)
	}

	err = a.client.AddRealmRoleComposite(context.Background(), a.token.AccessToken, realmName, role.Composite, []gocloak.Role{*persRole})
	if err != nil {
		return fmt.Errorf("failed to add realm role composite: %w", err)
	}

	log.Info("Keycloak roles has been created")

	return nil
}

func (a GoCloakAdapter) CreatePrimaryRealmRole(realmName string, role *dto.PrimaryRealmRole) (string, error) {
	log := a.log.WithValues("realm name", realmName, keycloakApiParamRole, role)
	log.Info("Start create realm roles in Keycloak...")

	realmRole := gocloak.Role{
		Name:        &role.Name,
		Description: &role.Description,
		Attributes:  &role.Attributes,
		Composite:   &role.IsComposite,
	}

	id, err := a.client.CreateRealmRole(context.Background(), a.token.AccessToken, realmName, realmRole)
	if err != nil {
		return "", errors.Wrap(err, "unable to create realm role")
	}

	if role.IsComposite && len(role.Composites) > 0 {
		compositeRoles := make([]gocloak.Role, 0, len(role.Composites))

		for _, composite := range role.Composites {
			var compositeRole *gocloak.Role

			compositeRole, err = a.client.GetRealmRole(context.Background(), a.token.AccessToken, realmName, composite)
			if err != nil {
				return "", errors.Wrap(err, "unable to get realm role")
			}

			compositeRoles = append(compositeRoles, *compositeRole)
		}

		if len(compositeRoles) > 0 {
			if err = a.client.AddRealmRoleComposite(context.Background(), a.token.AccessToken, realmName,
				role.Name, compositeRoles); err != nil {
				return "", errors.Wrap(err, "unable to add role composite")
			}
		}
	}

	log.Info("Keycloak roles has been created")

	return id, nil
}

func (a GoCloakAdapter) GetOpenIdConfig(realm *dto.Realm) (string, error) {
	log := a.log.WithValues("realm dto", realm)
	log.Info("Start get openid configuration...")

	resp, err := a.client.RestyClient().R().
		SetPathParams(map[string]string{
			keycloakApiParamRealm: realm.Name,
		}).
		Get(a.buildPath(openIdConfig))
	if err != nil {
		return "", fmt.Errorf("request get open id config failed: %w", err)
	}

	res := resp.String()

	log.Info("End get openid configuration", "openIdConfig", res)

	return res, nil
}

func (a GoCloakAdapter) PutDefaultIdp(realm *dto.Realm) error {
	log := a.log.WithValues("realm dto", realm)
	log.Info("Start put default IdP...")

	execution, err := a.getIdPRedirectExecution(realm)
	if err != nil {
		return err
	}

	if execution.AuthenticationConfig != "" {
		if err = a.updateRedirectConfig(realm, execution.AuthenticationConfig); err != nil {
			return fmt.Errorf("failed to update redirect config: %w", err)
		}

		log.Info("Default Identity Provider Redirector was successfully updated")

		return nil
	}

	err = a.createRedirectConfig(realm, execution.Id)
	if err != nil {
		return err
	}

	log.Info("Default Identity Provider Redirector was successfully configured")

	return nil
}

func (a GoCloakAdapter) getIdPRedirectExecution(realm *dto.Realm) (*api.SimpleAuthExecution, error) {
	exs, err := a.getBrowserExecutions(realm)
	if err != nil {
		return nil, err
	}

	return getIdPRedirector(exs)
}

func getIdPRedirector(executions []api.SimpleAuthExecution) (*api.SimpleAuthExecution, error) {
	for _, ex := range executions {
		if ex.ProviderId == "identity-provider-redirector" {
			return &ex, nil
		}
	}

	return nil, errors.New("identity provider not found")
}

func (a GoCloakAdapter) createRedirectConfig(realm *dto.Realm, eId string) error {
	resp, err := a.startRestyRequest().
		SetPathParams(map[string]string{
			keycloakApiParamRealm: realm.Name,
			keycloakApiParamId:    eId,
		}).
		SetBody(map[string]interface{}{
			keycloakApiParamAlias: "edp-sso",
			"config": map[string]string{
				"defaultProvider": realm.SsoRealmName,
			},
		}).
		Post(a.buildPath(authExecutionConfig))
	if err != nil {
		return errors.Wrap(err, "error during resty request")
	}

	if resp.StatusCode() != http.StatusCreated {
		return errors.Errorf("response is not ok by create redirect config: Status: %v", resp.Status())
	}

	if !realm.SsoAutoRedirectEnabled {
		resp, err := a.startRestyRequest().
			SetPathParams(map[string]string{keycloakApiParamRealm: realm.Name}).
			SetBody(map[string]string{
				keycloakApiParamId: eId,
				"requirement":      "DISABLED",
			}).
			Put(a.buildPath(authExecutions))
		if err != nil {
			return errors.Wrap(err, "error during resty request")
		}

		if resp.StatusCode() != http.StatusAccepted {
			return errors.Errorf("response is not ok by create redirect config: Status: %v", resp.Status())
		}
	}

	return nil
}

func (a GoCloakAdapter) getBrowserExecutions(realm *dto.Realm) ([]api.SimpleAuthExecution, error) {
	res := make([]api.SimpleAuthExecution, 0)

	resp, err := a.startRestyRequest().
		SetPathParams(map[string]string{
			keycloakApiParamRealm: realm.Name,
		}).
		SetResult(&res).
		Get(a.buildPath(authExecutions))
	if err != nil {
		return nil, fmt.Errorf("request get browser executions failed: %w", err)
	}

	if resp.StatusCode() != http.StatusOK {
		return res, fmt.Errorf("response is not ok by get browser executions: Status: %v", resp.Status())
	}

	return res, nil
}

func (a GoCloakAdapter) prepareProtocolMapperMaps(
	client *dto.Client,
	clientID string,
	claimedMappers []gocloak.ProtocolMapperRepresentation,
) (
	currentMappersMap,
	claimedMappersMap map[string]gocloak.ProtocolMapperRepresentation,
	resultErr error,
) {
	currentMappers, err := a.GetClientProtocolMappers(client, clientID)
	if err != nil {
		resultErr = errors.Wrap(err, "unable to get client protocol mappers")
		return
	}

	currentMappersMap = make(map[string]gocloak.ProtocolMapperRepresentation)
	claimedMappersMap = make(map[string]gocloak.ProtocolMapperRepresentation)
	// build maps to optimize comparing loops
	for i, m := range currentMappers {
		currentMappersMap[*m.Name] = currentMappers[i]
	}

	for i, m := range claimedMappers {
		// this block needed to fix 500 error response from server and for proper work of DeepEqual
		if m.Config == nil || *m.Config == nil {
			claimedMappers[i].Config = &map[string]string{}
		}

		claimedMappersMap[*m.Name] = claimedMappers[i]
	}

	return
}

func (a GoCloakAdapter) mapperNeedsToBeCreated(
	claimed *gocloak.ProtocolMapperRepresentation,
	currentMappersMap map[string]gocloak.ProtocolMapperRepresentation,
	realmName,
	clientID string,
) error {
	if _, ok := currentMappersMap[*claimed.Name]; !ok { // not exists in kc, must be created
		if _, err := a.client.CreateClientProtocolMapper(context.Background(), a.token.AccessToken,
			realmName, clientID, *claimed); err != nil {
			return errors.Wrap(err, "unable to client create protocol mapper")
		}
	}

	return nil
}

func (a GoCloakAdapter) mapperNeedsToBeUpdated(
	claimed *gocloak.ProtocolMapperRepresentation,
	currentMappersMap map[string]gocloak.ProtocolMapperRepresentation,
	realmName,
	clientID string,
) error {
	if current, ok := currentMappersMap[*claimed.Name]; ok { // claimed exists in current state, must be checked for update
		claimed.ID = current.ID                   // set id from current entity to claimed for proper DeepEqual comparison
		if !reflect.DeepEqual(claimed, current) { // mappers is not equal, needs to update
			if err := a.client.UpdateClientProtocolMapper(context.Background(), a.token.AccessToken,
				realmName, clientID, *claimed.ID, *claimed); err != nil {
				return errors.Wrap(err, "unable to update client protocol mapper")
			}
		}
	}

	return nil
}

func (a GoCloakAdapter) SyncClientProtocolMapper(
	client *dto.Client, claimedMappers []gocloak.ProtocolMapperRepresentation, addOnly bool) error {
	log := a.log.WithValues("clientId", client.ClientId)
	log.Info("Start put Client protocol mappers...")

	clientID, err := a.GetClientID(client.ClientId, client.RealmName)
	if err != nil {
		return errors.Wrap(err, "unable to get client id")
	}
	// prepare mapper entity maps for simplifying comparison procedure
	currentMappersMap, claimedMappersMap, err := a.prepareProtocolMapperMaps(client, clientID, claimedMappers)
	if err != nil {
		return errors.Wrap(err, "unable to prepare protocol mapper maps")
	}
	// compare actual client protocol mappers from keycloak to desired mappers, and sync them
	for _, claimed := range claimedMappers {
		if err := a.mapperNeedsToBeCreated(&claimed, currentMappersMap, client.RealmName, clientID); err != nil {
			return errors.Wrap(err, "error during mapperNeedsToBeCreated")
		}

		if err := a.mapperNeedsToBeUpdated(&claimed, currentMappersMap, client.RealmName, clientID); err != nil {
			return errors.Wrap(err, "error during mapperNeedsToBeUpdated")
		}
	}

	if !addOnly {
		for _, kc := range currentMappersMap {
			if _, ok := claimedMappersMap[*kc.Name]; !ok { // current mapper not exists in claimed, must be deleted
				if err := a.client.DeleteClientProtocolMapper(context.Background(), a.token.AccessToken, client.RealmName,
					clientID, *kc.ID); err != nil {
					return errors.Wrap(err, "unable to delete client protocol mapper")
				}
			}
		}
	}

	log.Info("Client protocol mapper was successfully configured!")

	return nil
}

func (a GoCloakAdapter) GetClientProtocolMappers(client *dto.Client,
	clientID string) ([]gocloak.ProtocolMapperRepresentation, error) {
	var mappers []gocloak.ProtocolMapperRepresentation

	resp, err := a.client.RestyClient().R().
		SetAuthToken(a.token.AccessToken).
		SetHeader(contentTypeHeader, contentTypeJson).
		SetPathParams(map[string]string{
			keycloakApiParamRealm: client.RealmName,
			keycloakApiParamId:    clientID,
		}).
		SetResult(&mappers).Get(a.buildPath(getClientProtocolMappers))
	if err != nil {
		return nil, errors.Wrap(err, "failed to get client protocol mappers")
	}

	if resp.IsError() {
		return nil, errors.New(resp.String())
	}

	return mappers, nil
}

func (a GoCloakAdapter) checkError(err error, response *resty.Response) error {
	if err != nil {
		return errors.Wrap(err, "response error")
	}

	if response == nil {
		return errors.New("empty response")
	}

	if response.IsError() {
		return errors.Errorf("status: %s, body: %s", response.Status(), response.String())
	}

	return nil
}
