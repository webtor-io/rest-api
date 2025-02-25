package services

import (
	"os"
	"path/filepath"
	"sync"

	"github.com/pkg/errors"

	log "github.com/sirupsen/logrus"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"
)

type K8SClient struct {
	cl   *kubernetes.Clientset
	err  error
	once sync.Once
}

func NewK8SClient() *K8SClient {
	return &K8SClient{}
}

func (s *K8SClient) get() (*kubernetes.Clientset, error) {
	kubeconfig := filepath.Join(os.Getenv("HOME"), ".kube", "config")
	log.Infof("checking local kubeconfig path=%s", kubeconfig)
	var config *rest.Config
	if _, err := os.Stat(kubeconfig); err == nil {
		log.WithField("kubeconfig", kubeconfig).Info("loading config from file (local mode)")
		config, err = clientcmd.BuildConfigFromFlags("", kubeconfig)
		if err != nil {
			return nil, errors.Wrap(err, "failed to make config")
		}
	} else {
		log.Info("loading config from cluster (cluster mode)")
		config, err = rest.InClusterConfig()
		if err != nil {
			return nil, errors.Wrap(err, "failed to make config")
		}
	}
	config.Burst = 100
	config.QPS = -1
	return kubernetes.NewForConfig(config)
}

func (s *K8SClient) Get() (*kubernetes.Clientset, error) {
	s.once.Do(func() {
		s.cl, s.err = s.get()
	})
	return s.cl, s.err
}
