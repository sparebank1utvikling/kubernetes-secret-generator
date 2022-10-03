package secret

import (
	"bytes"
	"crypto/rand"
	"encoding/base32"
	"encoding/base64"
	"encoding/hex"
	"fmt"
	"strings"
	"time"

	"github.com/go-logr/logr"
	corev1 "k8s.io/api/core/v1"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
)

type StringGenerator struct {
	log logr.Logger
}

type secretConfig struct {
	instance     *corev1.Secret
	key          string
	length       int
	isByteLength bool
}

func (pg StringGenerator) generateData(instance *corev1.Secret) (reconcile.Result, error) {
	toGenerate := instance.Annotations[AnnotationSecretAutoGenerate]

	genKeys := strings.Split(toGenerate, ",")

	if err := ensureUniqueness(genKeys); err != nil {
		return reconcile.Result{}, err
	}

	return pg.regenerateKeysWhereRequired(instance, genKeys)
}

func (pg StringGenerator) generateRandomSecret(conf secretConfig) error {
	key := conf.key
	instance := conf.instance
	length := conf.length
	isByteLength := conf.isByteLength

	encoding, err := getEncodingFromAnnotation(DefaultEncoding(), instance.Annotations)
	if err != nil {
		return err
	}
	value, err := GenerateRandomString(length, encoding, isByteLength)
	if err != nil {
		return err
	}
	const templateKey = "${SECRET}"
	template, err := getTemplateFromAnnotation(templateKey, instance.Annotations)
	if err != nil {
		return err
	}

	value = bytes.ReplaceAll([]byte(template), []byte(templateKey), value)

	instance.Data[key] = value

	pg.log.Info("set field of instance to new randomly generated instance", "bytes", len(value), "field", key, "encoding", encoding)

	return nil
}

// GenerateRandomString generates a random string of given length and with given encoding.
// If lenBytes is true, resultring string will not be trimmed
func GenerateRandomString(length int, encoding string, lenBytes bool) ([]byte, error) {
	b := make([]byte, length)
	_, err := rand.Read(b)
	if err != nil {
		return []byte{}, err
	}

	var encodedString string
	switch encoding {
	case "base64url":
		encodedString = base64.URLEncoding.EncodeToString(b)
	case "raw":
		return b, nil
	case "base32":
		encodedString = base32.StdEncoding.EncodeToString(b)
	case "hex":
		encodedString = hex.EncodeToString(b)
	default:
		encodedString = base64.StdEncoding.EncodeToString(b)
	}
	// if length was specified with B suffix, don't trim result string
	if lenBytes {
		return []byte(encodedString), nil
	}

	return []byte(encodedString[0:length]), nil
}

// ensure elements in input array are unique
func ensureUniqueness(a []string) error {
	set := map[string]bool{}
	for _, e := range a {
		if set[e] {
			return fmt.Errorf("duplicate element %s found", e)
		}
		set[e] = true
	}
	return nil
}

func contains(s []string, e string) bool {
	for _, a := range s {
		if a == e {
			return true
		}
	}
	return false
}

func (pg StringGenerator) regenerateKeysWhereRequired(instance *corev1.Secret, genKeys []string) (reconcile.Result, error) {
	var regenKeys []string

	if _, ok := instance.Annotations[AnnotationSecretSecure]; !ok && RegenerateInsecure() {
		pg.log.Info("instance was generated by a cryptographically insecure PRNG")
		regenKeys = genKeys // regenerate all keys
	} else if regenerate, ok := instance.Annotations[AnnotationSecretRegenerate]; ok {
		pg.log.Info("removing regenerate annotation from instance")
		delete(instance.Annotations, AnnotationSecretRegenerate)

		if regenerate == "yes" {
			regenKeys = genKeys
		} else {
			regenKeys = strings.Split(regenerate, ",") // regenerate requested keys
		}
	}

	length, err := GetLengthFromAnnotation(DefaultLength(), instance.Annotations)
	if err != nil {
		return reconcile.Result{}, err
	}

	parsedLength, isByteLength, err := ParseByteLength(DefaultLength(), length)
	if err != nil {
		return reconcile.Result{}, err
	}

	generatedCount := 0
	for _, key := range genKeys {
		if len(instance.Data[key]) != 0 && !contains(regenKeys, key) {
			// dont generate key if it already has a value
			// and is not queued for regeneration
			continue
		}
		generatedCount++

		err = pg.generateRandomSecret(secretConfig{instance, key, parsedLength, isByteLength})
		if err != nil {
			pg.log.Error(err, "could not generate new random string")
			return reconcile.Result{RequeueAfter: time.Second * 30}, err
		}
	}

	pg.log.Info("generated secrets", "count", generatedCount)

	if generatedCount == len(genKeys) {
		// all keys have been generated by this instance
		instance.Annotations[AnnotationSecretSecure] = "yes"
	}

	return reconcile.Result{}, nil
}
