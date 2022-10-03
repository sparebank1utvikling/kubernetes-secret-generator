package secret

import (
	"context"
	errstd "errors"
	"reflect"
	"strconv"
	"time"

	"github.com/spf13/viper"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	logf "sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/manager"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
	"sigs.k8s.io/controller-runtime/pkg/source"
)

const ByteSuffix = "b"

var log = logf.Log.WithName("controller_secret")

func RegenerateInsecure() bool {
	return viper.GetBool("regenerate-insecure")
}

func DefaultLength() int {
	return viper.GetInt("secret-length")
}

func DefaultEncoding() string {
	return viper.GetString("secret-encoding")
}

func SSHKeyLength() int {
	return viper.GetInt("ssh-key-length")
}

// Add creates a new Secret Controller and adds it to the Manager. The Manager will set fields on the Controller
// and Start it when the Manager is Started.
func Add(mgr manager.Manager) error {
	return add(mgr, NewReconciler(mgr))
}

// NewReconciler returns a new reconcile.Reconciler
func NewReconciler(mgr manager.Manager) reconcile.Reconciler {
	return &ReconcileSecret{client: mgr.GetClient(), scheme: mgr.GetScheme()}
}

// add adds a new Controller to mgr with r as the reconcile.Reconciler
func add(mgr manager.Manager, r reconcile.Reconciler) error {
	// Create a new controller
	c, err := controller.New("secret-controller", mgr, controller.Options{Reconciler: r})
	if err != nil {
		return err
	}

	// Watch for changes to primary resource Secret
	err = c.Watch(&source.Kind{Type: &corev1.Secret{}}, &handler.EnqueueRequestForObject{})
	if err != nil {
		return err
	}

	return nil
}

// blank assignment to verify that ReconcileSecret implements reconcile.Reconciler
var _ reconcile.Reconciler = &ReconcileSecret{}

// ReconcileSecret reconciles a Secret object
type ReconcileSecret struct {
	// This Client, initialized using mgr.Client() above, is a split Client
	// that reads objects from the cache and writes to the apiserver
	client client.Client
	scheme *runtime.Scheme
}

// Reconcile reads that state of the cluster for a Secret object and makes changes based on the state read
// and what is in the Secret.Spec
// Note:
// The Controller will requeue the Request to be processed again if the returned error is non-nil or
// Result.Requeue is true, otherwise upon completion it will remove the work from the queue.
func (r *ReconcileSecret) Reconcile(request reconcile.Request) (reconcile.Result, error) {
	reqLogger := log.WithValues("Request.Namespace", request.Namespace, "Request.Name", request.Name)
	reqLogger.Info("Reconciling Secret")

	// Fetch the Secret instance
	instance := &corev1.Secret{}
	err := r.client.Get(context.TODO(), request.NamespacedName, instance)
	if err != nil {
		if errors.IsNotFound(err) {
			// Request object not found, could have been deleted after reconcile request.
			// Owned objects are automatically garbage collected. For additional cleanup logic use finalizers.
			// Return and don't requeue
			return reconcile.Result{}, nil
		}
		// Error reading the object - requeue the request.
		return reconcile.Result{}, err
	}

	desired := instance.DeepCopy()

	sType := Type(desired.Annotations[AnnotationSecretType])
	if err = sType.Validate(); err != nil {
		if _, ok := desired.Annotations[AnnotationSecretAutoGenerate]; !ok && sType == "" {
			// return if secret has no type and no autogenerate annotation
			return reconcile.Result{}, nil
		}

		// keep backwards compatibility by defaulting to string type
		desired.Annotations[AnnotationSecretType] = string(TypeString)
		sType = TypeString
	}

	reqLogger = reqLogger.WithValues("type", sType)
	reqLogger.Info("instance is autogenerated")

	if desired.Data == nil {
		desired.Data = make(map[string][]byte)
	}

	var generator Generator
	switch sType {
	case TypeSSHKeypair:
		generator = SSHKeypairGenerator{
			log: reqLogger.WithValues("type", TypeSSHKeypair),
		}
	case TypeString:
		generator = StringGenerator{
			log: reqLogger.WithValues("type", TypeString),
		}
	case TypeBasicAuth:
		generator = BasicAuthGenerator{
			log: reqLogger.WithValues("type", TypeBasicAuth),
		}
	default:
		// default case to prevent potential nil-pointer
		reqLogger.Error(errstd.New("SecretTypeNotSpecified"), "Secret type was not specified")
		return reconcile.Result{Requeue: true}, errstd.New("SecretTypeNotSpecified")
	}

	res, err := generator.generateData(desired)
	if err != nil {
		return res, err
	}

	if !reflect.DeepEqual(instance.Annotations, desired.Annotations) ||
		!reflect.DeepEqual(instance.Data, desired.Data) {
		reqLogger.Info("updating secret")

		desired.Annotations[AnnotationSecretAutoGeneratedAt] = time.Now().Format(time.RFC3339)
		err := r.client.Update(context.Background(), desired)
		if err != nil {
			reqLogger.Error(err, "could not update secret")
			return reconcile.Result{Requeue: true}, err
		}
	}

	return reconcile.Result{}, nil
}

func GetLengthFromAnnotation(fallback int, annotations map[string]string) (string, error) {
	l := fallback

	if val, ok := annotations[AnnotationSecretLength]; ok {
		return val, nil
	}
	return strconv.Itoa(l), nil
}

func getEncodingFromAnnotation(fallback string, annotations map[string]string) (string, error) {
	if val, ok := annotations[AnnotationSecretEncoding]; ok {
		return val, nil
	}
	return fallback, nil
}

func getTemplateFromAnnotation(fallback string, annotations map[string]string) (string, error) {
	if val, ok := annotations[AnnotationSecretTemplate]; ok {
		return val, nil
	}
	return fallback, nil
}
