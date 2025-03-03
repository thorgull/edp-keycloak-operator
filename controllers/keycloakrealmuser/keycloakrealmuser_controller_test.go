package keycloakrealmuser

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/suite"
	coreV1 "k8s.io/api/core/v1"
	k8sErrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	utilruntime "k8s.io/apimachinery/pkg/util/runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	keycloakApi "github.com/epam/edp-keycloak-operator/api/v1"
	"github.com/epam/edp-keycloak-operator/controllers/helper"
	"github.com/epam/edp-keycloak-operator/pkg/client/keycloak/adapter"
	"github.com/epam/edp-keycloak-operator/pkg/client/keycloak/mock"
)

func TestNewReconcile_Init(t *testing.T) {
	c := NewReconcile(nil, mock.NewLogr(), &helper.Mock{})
	if c.client != nil {
		t.Fatal("something went wrong")
	}
}

type TestControllerSuite struct {
	suite.Suite
	namespace   string
	scheme      *runtime.Scheme
	realmName   string
	kcRealmUser *keycloakApi.KeycloakRealmUser
	k8sClient   client.Client
	helper      *helper.Mock
	kcRealm     *keycloakApi.KeycloakRealm
	kClient     *adapter.Mock
	adapterUser *adapter.KeycloakUser
}

func (e *TestControllerSuite) SetupTest() {
	e.namespace = "ns"
	e.scheme = runtime.NewScheme()
	utilruntime.Must(keycloakApi.AddToScheme(e.scheme))
	e.realmName = "realmName"
	e.kcRealmUser = &keycloakApi.KeycloakRealmUser{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "user321",
			Namespace: e.namespace,
		},
		TypeMeta: metav1.TypeMeta{
			Kind:       "KeycloakRealmUser",
			APIVersion: "v1.edp.epam.com/v1",
		},
		Spec: keycloakApi.KeycloakRealmUserSpec{
			Email:    "usr@gmail.com",
			Username: "user.g1",
			Realm:    e.realmName,
		},
		Status: keycloakApi.KeycloakRealmUserStatus{
			Value: helper.StatusOK,
		},
	}
	e.k8sClient = fake.NewClientBuilder().WithScheme(e.scheme).WithRuntimeObjects(e.kcRealmUser).Build()
	e.helper = &helper.Mock{}
	e.kcRealm = &keycloakApi.KeycloakRealm{
		Spec: keycloakApi.KeycloakRealmSpec{
			RealmName: e.realmName,
		},
	}
	e.kClient = &adapter.Mock{}
	e.adapterUser = &adapter.KeycloakUser{
		Username:            e.kcRealmUser.Spec.Username,
		Groups:              e.kcRealmUser.Spec.Groups,
		Roles:               e.kcRealmUser.Spec.Roles,
		RequiredUserActions: e.kcRealmUser.Spec.RequiredUserActions,
		LastName:            e.kcRealmUser.Spec.LastName,
		FirstName:           e.kcRealmUser.Spec.FirstName,
		EmailVerified:       e.kcRealmUser.Spec.EmailVerified,
		Enabled:             e.kcRealmUser.Spec.Enabled,
		Email:               e.kcRealmUser.Spec.Email,
	}
}

func (e *TestControllerSuite) TestNewReconcile() {
	e.helper.On("GetOrCreateRealmOwnerRef", e.kcRealmUser, &e.kcRealmUser.ObjectMeta).Return(e.kcRealm, nil)
	e.helper.On("CreateKeycloakClientForRealm", e.kcRealm).Return(e.kClient, nil)

	r := Reconcile{
		helper: e.helper,
		log:    mock.NewLogr(),
		client: e.k8sClient,
	}

	e.kClient.On("SyncRealmUser", e.realmName, e.adapterUser, false).Return(nil)
	e.helper.On("UpdateStatus", e.kcRealmUser).Return(nil)

	_, err := r.Reconcile(context.Background(), reconcile.Request{NamespacedName: types.NamespacedName{
		Namespace: e.namespace,
		Name:      e.kcRealmUser.Name,
	}})
	assert.NoError(e.T(), err)

	var checkUser keycloakApi.KeycloakRealmUser
	err = e.k8sClient.Get(context.Background(),
		types.NamespacedName{Name: e.kcRealmUser.Name, Namespace: e.kcRealmUser.Namespace}, &checkUser)
	assert.Error(e.T(), err, "user is not deleted")
	assert.True(e.T(), k8sErrors.IsNotFound(err), "wrong error returned")
}

func (e *TestControllerSuite) TestReconcileKeep() {
	e.kcRealmUser.Spec.KeepResource = true
	e.k8sClient = fake.NewClientBuilder().WithScheme(e.scheme).WithRuntimeObjects(e.kcRealmUser).Build()

	logger := mock.NewLogr()

	e.helper.On("GetOrCreateRealmOwnerRef", e.kcRealmUser, &e.kcRealmUser.ObjectMeta).Return(e.kcRealm, nil)
	e.helper.On("CreateKeycloakClientForRealm", e.kcRealm).Return(e.kClient, nil)
	e.helper.On("TryToDelete", e.kcRealmUser,
		makeTerminator(e.realmName, e.kcRealmUser.Spec.Username, e.kClient, logger), finalizer).
		Return(false, nil)
	e.helper.On("UpdateStatus", e.kcRealmUser).Return(nil)

	r := Reconcile{
		helper: e.helper,
		log:    logger,
		client: e.k8sClient,
	}

	e.kClient.On("SyncRealmUser", e.realmName, e.adapterUser, false).Return(nil)

	_, err := r.Reconcile(context.Background(), reconcile.Request{NamespacedName: types.NamespacedName{
		Namespace: e.namespace,
		Name:      e.kcRealmUser.Name,
	}})
	assert.NoError(e.T(), err)

	var checkUser keycloakApi.KeycloakRealmUser
	err = e.k8sClient.Get(context.Background(),
		types.NamespacedName{Name: e.kcRealmUser.Name, Namespace: e.kcRealmUser.Namespace}, &checkUser)
	assert.NoError(e.T(), err)
}

func TestAdapterTestSuite(t *testing.T) {
	suite.Run(t, new(TestControllerSuite))
}

func (e *TestControllerSuite) TestGetPassword() {
	e.kcRealmUser.Spec.PasswordSecret.Name = "my-secret"
	e.kcRealmUser.Spec.PasswordSecret.Key = "my-key"

	secret := &coreV1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "my-secret",
			Namespace: e.namespace,
		},
		Data: map[string][]byte{
			"my-key": []byte("my-secret-password"),
		},
	}

	e.scheme.AddKnownTypes(coreV1.SchemeGroupVersion, secret)

	e.k8sClient = fake.NewClientBuilder().WithScheme(e.scheme).WithRuntimeObjects(e.kcRealmUser, secret).Build()

	logger := mock.NewLogr()

	r := &Reconcile{
		client: e.k8sClient,
		log:    logger,
	}

	password, err := r.getPassword(context.Background(), e.kcRealmUser)
	assert.NoError(e.T(), err)
	assert.Equal(e.T(), "my-secret-password", password)

	e.kcRealmUser.Spec.PasswordSecret.Key = "non-existent-key"
	password, err = r.getPassword(context.Background(), e.kcRealmUser)
	assert.Error(e.T(), err)
	assert.Equal(e.T(), "", password)

	e.kcRealmUser.Spec.PasswordSecret.Name = "non-existent-secret"
	password, err = r.getPassword(context.Background(), e.kcRealmUser)
	assert.Error(e.T(), err)
	assert.Equal(e.T(), "", password)

	e.kcRealmUser.Spec.PasswordSecret.Name = ""
	e.kcRealmUser.Spec.Password = "spec-password"
	password, err = r.getPassword(context.Background(), e.kcRealmUser)
	assert.NoError(e.T(), err)
	assert.Equal(e.T(), "spec-password", password)
}
