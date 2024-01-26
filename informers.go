package main

import (
	"context"

	log "github.com/sirupsen/logrus"
	v1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/informers"
	"k8s.io/client-go/tools/cache"
)

func startInformers(k8s *k8sClient) {
	factory := informers.NewSharedInformerFactory(k8s.clientset, 0)
	namespaceInformer := factory.Core().V1().Namespaces().Informer()
	secretInformer := factory.Core().V1().Secrets().Informer()
	serviceAccountInformer := factory.Core().V1().ServiceAccounts().Informer()
	stopper := make(chan struct{})
	defer close(stopper)

	secretInformer.AddEventHandler(cache.ResourceEventHandlerFuncs{
		AddFunc: func(obj interface{}) {
			secret := obj.(*v1.Secret)
			if secret.Name == configSecretName && secret.Namespace == configSecretNamespace {
				log.Debugf("Original secret [%s] in namespace [%s]", secret.Name, secret.Namespace)
			}
		},
		DeleteFunc: func(obj interface{}) {
			secret := obj.(*v1.Secret)
			if secret.Name == configSecretName && secret.Namespace != configSecretNamespace {
				log.Debugf("Deleted secret [%s] in namespace [%s]", secret.Name, secret.Namespace)

				namespace := secret.Namespace
				namespaceObj, err := k8s.clientset.CoreV1().Namespaces().Get(context.Background(), namespace, metav1.GetOptions{})
				if err != nil {
					log.Panic(err)
				}

				// if namespace is excluded do nothing and only log
				if namespaceIsExcluded(*namespaceObj) {
					log.Infof("[%s] Namespace skipped", namespaceObj.Name)
					return
				}

				// if namespace is terminate do nothing and log only
				if namespaceObj.Status.Phase == "Terminating" {
					log.Debugf("[%s] namespace is in phase %s", namespace, namespaceObj.Status.Phase)
					return
				}

				log.Debugf("[%s] Start processing secret", namespace)
				// for each namespace, make sure the dockerconfig secret exists
				err = processSecret(k8s, namespace)

				if err != nil {
					// if has error in processing secret, should skip processing service account
					log.Error(err)
					return
				}

				log.Debugf("[%s] Start processing service accounts", namespace)

				// provision deleted secret to all managed service accounts
				if configAllServiceAccount || len(configServiceAccounts) > 0 {
					provisionManagedServiceAccounts(k8s, namespace)
					return
				}

				// provision default service account
				sa, err := k8s.clientset.CoreV1().ServiceAccounts(namespace).Get(context.Background(), defaultServiceAccountName, metav1.GetOptions{})
				if err != nil {
					log.Error(err)
				}
				err = processServiceAccount(k8s, namespace, sa)
				if err != nil {
					log.Error(err)
				}
			}
		},
		UpdateFunc: func(oldObj, newObj interface{}) {
			secret := oldObj.(*v1.Secret)
			if secret.Name == configSecretName && secret.Namespace == configSecretNamespace {
				log.Debugf("Updated secret [%s] in namespace [%s]", secret.Name, secret.Namespace)
				log.Debug("Running update loop")
				loop(k8s)
			}
		},
	})

	namespaceInformer.AddEventHandler(cache.ResourceEventHandlerFuncs{
		AddFunc: func(obj interface{}) {
			var err error
			ns := obj.(*v1.Namespace)
			namespace := ns.Name
			log.Debugf("[%s] Namespace discovered", namespace)
			if namespaceIsExcluded(*ns) {
				log.Infof("[%s] Namespace skipped", namespace)
				return
			}

			log.Debugf("[%s] Start processing secret", namespace)
			// for each namespace, make sure the dockerconfig secret exists
			err = processSecret(k8s, namespace)
			if err != nil {
				// if has error in processing secret, should skip processing service account
				log.Error(err)
			}
		},
		DeleteFunc: func(obj interface{}) {
			ns := obj.(*v1.Namespace)
			namespace := ns.Name
			log.Debugf("[%s] Namespace deleted", namespace)
		},
	})
	serviceAccountInformer.AddEventHandler(cache.ResourceEventHandlerFuncs{
		AddFunc: func(obj interface{}) {
			// var err error
			sa := obj.(*v1.ServiceAccount)
			serviceAccount := sa.Name
			namespace := sa.Namespace
			// check if namespace of service account exists
			namespaceObj, err := k8s.clientset.CoreV1().Namespaces().Get(context.Background(), namespace, metav1.GetOptions{})
			if err != nil {
				log.Panic(err)
			}
			log.Infof("[%s] ServiceAccount [%s] discovered", sa.Namespace, serviceAccount)
			if namespaceIsExcluded(*namespaceObj) {
				log.Infof("[%s] Namespace excluded", namespace)
				return
			}

			log.Debugf("[%s] Start processing service account [%s]", namespace, serviceAccount)
			// get default service account, and patch image pull secret if not exist
			err = processServiceAccount(k8s, namespace, sa)
			if err != nil {
				log.Error(err)
			}
		},
	})
	log.Info("Namespace informer started")
	go namespaceInformer.Run(stopper)
	log.Info("ServiceAccount informer started")
	go serviceAccountInformer.Run(stopper)
	log.Info("Secret informer started")
	secretInformer.Run(stopper)
}
