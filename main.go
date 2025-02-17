package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	log "github.com/sirupsen/logrus"
	v1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"
	"k8s.io/client-go/util/homedir"
)

var (
	// Config
	configForce                bool          = true
	configDebug                bool          = false
	configManagedOnly          bool          = false
	configRunOnce              bool          = false
	configAllServiceAccount    bool          = false
	configDockerconfigjson     string        = ""
	configDockerConfigJSONPath string        = ""
	configSecretName           string        = "image-pull-secret"       // default to image-pull-secret
	configSecretNamespace      string        = "imagepullsecret-patcher" // default to imagepullsecret-patcher
	configExcludedNamespaces   string        = ""
	configServiceAccounts      string        = defaultServiceAccountName
	configUseInformers         bool          = true
	configLoopDuration         time.Duration = 10 * time.Second
	configRunningInCluster     bool          = true
	dockerConfigJSON           string
)

const (
	annotationImagepullsecretPatcherExclude = "k8s.titansoft.com/imagepullsecret-patcher-exclude"
)

type k8sClient struct {
	clientset kubernetes.Interface
}

func main() {
	// parse flags
	flag.BoolVar(&configForce, "force", LookupEnvOrType("CONFIG_FORCE", configForce), "force to overwrite secrets when not match")
	flag.BoolVar(&configDebug, "debug", LookupEnvOrType("CONFIG_DEBUG", configDebug), "show DEBUG logs")
	flag.BoolVar(&configManagedOnly, "managedonly", LookupEnvOrType("CONFIG_MANAGEDONLY", configManagedOnly), "only modify secrets which are annotated as managed by imagepullsecret")
	flag.BoolVar(&configRunOnce, "runonce", LookupEnvOrType("CONFIG_RUNONCE", configRunOnce), "run a single update and exit instead of looping")
	flag.BoolVar(&configAllServiceAccount, "allserviceaccount", LookupEnvOrType("CONFIG_ALLSERVICEACCOUNT", configAllServiceAccount), "if false, patch just default service account; if true, list and patch all service accounts")
	flag.StringVar(&configDockerconfigjson, "dockerconfigjson", LookupEnvOrType("CONFIG_DOCKERCONFIGJSON", configDockerconfigjson), "json credential for authenicating container registry, exclusive with dockerconfigjsonpath")
	flag.StringVar(&configDockerConfigJSONPath, "dockerconfigjsonpath", LookupEnvOrType("CONFIG_DOCKERCONFIGJSONPATH", configDockerConfigJSONPath), "path to json file containing credentials for the registry to be distributed, exclusive with dockerconfigjson")
	flag.StringVar(&configSecretName, "secretname", LookupEnvOrType("CONFIG_SECRETNAME", configSecretName), "set name of managed secrets")
	flag.StringVar(&configSecretNamespace, "secretnamespace", LookupEnvOrType("CONFIG_SECRET_NAMESPACE", configSecretNamespace), "namespace where original secret can be found")
	flag.StringVar(&configExcludedNamespaces, "excluded-namespaces", LookupEnvOrType("CONFIG_EXCLUDED_NAMESPACES", configExcludedNamespaces), "comma-separated namespaces excluded from processing")
	flag.StringVar(&configServiceAccounts, "serviceaccounts", LookupEnvOrType("CONFIG_SERVICEACCOUNTS", configServiceAccounts), "comma-separated list of serviceaccounts to patch")
	flag.BoolVar(&configUseInformers, "use-informers", LookupEnvOrType("CONFIG_USE_INFORMERS", configUseInformers), "if true, k8s informers to detect when new namespace is created and then it will run patching process, if false it runs in a loop for all namespaces")
	flag.DurationVar(&configLoopDuration, "loop-duration", LookupEnvOrType("CONFIG_LOOP_DURATION", configLoopDuration), "String defining the loop duration")
	flag.BoolVar(&configRunningInCluster, "running-in-cluster", LookupEnvOrType("CONFIG_RUNNING_IN_CLUSTER", configRunningInCluster), "if false, will use kubeconfig and current context to connect to k8s API")
	flag.Parse()
	// setup logrus
	if configDebug {
		log.SetLevel(log.DebugLevel)
	}
	log.Info("Application started")
	// Validate input, as both of these being configured would have undefined behavior.
	if configDockerconfigjson != "" && configDockerConfigJSONPath != "" {
		log.Panic(fmt.Errorf("Cannot specify both `configdockerjson` and `configdockerjsonpath`"))
	}
	// create k8s clientset from in-cluster config
	var config *rest.Config
	var err error
	if configRunningInCluster {
		// create k8s config from in-cluster config
		config, err = rest.InClusterConfig()
		if err != nil {
			log.Panic(err)
		}
	} else {
		// create k8s config from local kubeconfig
		var kubeconfig *string
		var err error
		if home := homedir.HomeDir(); home != "" {
			kubeconfig = flag.String("kubeconfig", filepath.Join(home, ".kube", "config"), "(optional) absolute path to the kubeconfig file")
		} else {
			kubeconfig = flag.String("kubeconfig", "", "absolute path to the kubeconfig file")
		}
		config, err = clientcmd.BuildConfigFromFlags("", *kubeconfig)
		if err != nil {
			log.Panic(err)
		}
	}
	clientset, err := kubernetes.NewForConfig(config)
	if err != nil {
		log.Panic(err)
	}
	k8s := &k8sClient{
		clientset: clientset,
	}
	// Populate secret value to set
	dockerConfigJSON, err = getDockerConfigJSON()
	if err != nil {
		log.Panic(err)
	}
	if configUseInformers {
		log.Debug("Informer started")
		startInformers(k8s)
	} else {
		for {
			log.Debug("Loop started")
			loop(k8s)
			if configRunOnce {
				log.Info("Exiting after single loop per `CONFIG_RUNONCE`")
				os.Exit(0)
			}
			time.Sleep(configLoopDuration)
		}
	}
}

func loop(k8s *k8sClient) {
	var err error

	// Populate secret value to set
	dockerConfigJSON, err = getDockerConfigJSON()
	if err != nil {
		log.Panic(err)
	}

	// get all namespaces
	namespaces, err := k8s.clientset.CoreV1().Namespaces().List(context.Background(), metav1.ListOptions{})
	if err != nil {
		log.Panic(err)
	}
	log.Debugf("Got %d namespaces", len(namespaces.Items))

	for _, ns := range namespaces.Items {
		namespace := ns.Name
		if namespaceIsExcluded(ns) {
			log.Infof("[%s] Namespace skipped", namespace)
			continue
		}
		log.Debugf("[%s] Start processing secret", namespace)
		// for each namespace, make sure the dockerconfig secret exists
		err = processSecret(k8s, namespace)
		if err != nil {
			// if has error in processing secret, should skip processing service account
			log.Error(err)
			continue
		}
		// get default service account, and patch image pull secret if not exist
		log.Debugf("[%s] Start processing service account", namespace)
		if configAllServiceAccount || len(configServiceAccounts) > 0 {
			provisionManagedServiceAccounts(k8s, namespace)
			continue
		}

		sa, err := k8s.clientset.CoreV1().ServiceAccounts(namespace).Get(context.Background(), defaultServiceAccountName, metav1.GetOptions{})
		if err != nil {
			log.Error(err)
			continue
		}
		err = processServiceAccount(k8s, namespace, sa)
		if err != nil {
			log.Error(err)
			continue
		}
	}
}

func namespaceIsExcluded(ns v1.Namespace) bool {
	v, ok := ns.Annotations[annotationImagepullsecretPatcherExclude]
	if ok && v == "true" {
		return true
	}
	for _, ex := range strings.Split(configExcludedNamespaces, ",") {
		if ex == ns.Name {
			return true
		}
	}
	return false
}

func processSecret(k8s *k8sClient, namespace string) error {
	secret, err := k8s.clientset.CoreV1().Secrets(namespace).Get(context.Background(), configSecretName, metav1.GetOptions{})

	// secret is not found so let's provision one
	if errors.IsNotFound(err) {
		_, err := k8s.clientset.CoreV1().Secrets(namespace).Create(context.Background(), dockerconfigSecret(namespace), metav1.CreateOptions{})
		if err != nil {
			return fmt.Errorf("[%s] Failed to create secret: %v", namespace, err)
		}
		log.Infof("[%s] Created secret", namespace)
		return nil
	}

	// unknown error during get for secret
	if err != nil {
		return fmt.Errorf("[%s] Failed to GET secret: %v", namespace, err)
	}

	// secret with name equal to configSecretName is found
	if configManagedOnly && isManagedSecret(secret) {
		return fmt.Errorf("[%s] Secret is present but unmanaged", namespace)
	}

	// verify status of matching secret
	switch verifySecret(secret) {
	case secretOk:
		log.Debugf("[%s] Secret is valid", namespace)
	case secretWrongType, secretNoKey, secretDataNotMatch:
		if configForce {
			log.Warnf("[%s] Secret is not valid, overwritting now", namespace)
			err = k8s.clientset.CoreV1().Secrets(namespace).Delete(context.Background(), configSecretName, metav1.DeleteOptions{})
			if err != nil {
				return fmt.Errorf("[%s] Failed to delete secret [%s]: %v", namespace, configSecretName, err)
			}
			log.Warnf("[%s] Deleted secret [%s]", namespace, configSecretName)
			_, err = k8s.clientset.CoreV1().Secrets(namespace).Create(context.Background(), dockerconfigSecret(namespace), metav1.CreateOptions{})
			if err != nil {
				return fmt.Errorf("[%s] Failed to create secret: %v", namespace, err)
			}
			log.Infof("[%s] Created secret", namespace)
		} else {
			return fmt.Errorf("[%s] Secret is not valid, set --force to true to overwrite", namespace)
		}
	}
	return nil
}

func provisionManagedServiceAccounts(k8s *k8sClient, namespace string) {
	sas, err := k8s.clientset.CoreV1().ServiceAccounts(namespace).List(context.Background(), metav1.ListOptions{})
	if err != nil {
		log.Error(err)
		return
	}
	for _, sa := range sas.Items {
		err = processServiceAccount(k8s, namespace, &sa)
		if err != nil {
			log.Error(err)
			continue
		}
	}
}

func processServiceAccount(k8s *k8sClient, namespace string, serviceAccount *v1.ServiceAccount) error {
	if !configAllServiceAccount && stringNotInList(serviceAccount.Name, configServiceAccounts) {
		log.Debugf("[%s] Skip service account [%s]", namespace, serviceAccount.Name)
		return nil
	}

	if includeImagePullSecret(serviceAccount, configSecretName) {
		log.Debugf("[%s] ImagePullSecrets found", namespace)
		return nil
	}

	patch, err := getPatchString(serviceAccount, configSecretName)
	if err != nil {
		return fmt.Errorf("[%s] Failed to get patch string: %v", namespace, err)
	}

	_, err = k8s.clientset.CoreV1().ServiceAccounts(namespace).Patch(context.Background(), serviceAccount.Name, types.StrategicMergePatchType, patch, metav1.PatchOptions{})
	if err != nil {
		return fmt.Errorf("[%s] Failed to patch imagePullSecrets to service account [%s]: %v", namespace, serviceAccount.Name, err)
	}
	log.Infof("[%s] Patched imagePullSecrets to service account [%s]", namespace, serviceAccount.Name)

	return nil
}

func stringNotInList(a string, list string) bool {
	for _, b := range strings.Split(list, ",") {
		if b == a {
			return false
		}
	}
	return true
}
