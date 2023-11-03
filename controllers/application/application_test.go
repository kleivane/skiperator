package applicationcontroller_test

import (
	"context"
	"github.com/google/go-cmp/cmp"
	"github.com/imdario/mergo"
	skiperatorv1alpha1 "github.com/kartverket/skiperator/api/v1alpha1"
	"github.com/kartverket/skiperator/api/v1alpha1/podtypes"
	. "github.com/kartverket/skiperator/pkg/util"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	securityapi "istio.io/api/security/v1beta1"
	networkingv1beta1 "istio.io/client-go/pkg/apis/networking/v1beta1"
	"istio.io/client-go/pkg/apis/security/v1beta1"
	appsv1 "k8s.io/api/apps/v1"
	autoscalingv2 "k8s.io/api/autoscaling/v2"
	corev1 "k8s.io/api/core/v1"
	k8sv1 "k8s.io/api/core/v1"
	v1 "k8s.io/api/networking/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/intstr"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"time"
)

var _ = Describe("Applications", func() {
	var application skiperatorv1alpha1.Application
	const (
		timeout  = time.Second * 30
		duration = time.Second * 10
		interval = time.Millisecond * 250
	)

	Context("When an application is minimal", func() {
		It("should have created required resources", func() {
			appName := "minimal"
			ns := newNamespace()

			application = skiperatorv1alpha1.Application{
				ObjectMeta: metav1.ObjectMeta{
					Name:      appName,
					Namespace: ns.Name,
				},
				Spec: skiperatorv1alpha1.ApplicationSpec{
					Image: "image",
					Port:  8080,
				},
			}
			Expect(k8sClient.Create(ctx, &application)).Should(Succeed())
			deployment := &appsv1.Deployment{
				ObjectMeta: application.ObjectMeta,
			}
			Eventually(func() bool {
				err := k8sClient.Get(ctx, client.ObjectKeyFromObject(deployment), deployment)
				return err == nil
			}, timeout, interval).Should(BeTrue())
			Expect(deployment.Spec.Replicas).Should(Equal(PointTo(int32(1))))
			Expect(deployment.Spec.Selector.MatchLabels).Should(Equal(map[string]string{"app": "minimal"}))
			expectedTemplateObjectMeta := metav1.ObjectMeta{
				Labels:      map[string]string{"app": appName},
				Annotations: map[string]string{"argocd.argoproj.io/sync-options": "Prune=false", "prometheus.io/scrape": "true"},
			}

			Expect(deployment.Spec.Template.ObjectMeta).Should(Equal(expectedTemplateObjectMeta))
			Expect(deployment.Spec.Template.Spec.Volumes).Should(Equal([]k8sv1.Volume{{Name: "tmp", VolumeSource: k8sv1.VolumeSource{EmptyDir: &k8sv1.EmptyDirVolumeSource{}}}}))
			expectedContainer := []k8sv1.Container{{
				Name:  appName,
				Image: "image",
				Ports: []k8sv1.ContainerPort{{
					Name:          "main",
					ContainerPort: int32(8080),
					Protocol:      k8sv1.ProtocolTCP,
				}},
				VolumeMounts: []k8sv1.VolumeMount{{
					Name:      "tmp",
					MountPath: "/tmp",
				}},
				TerminationMessagePath:   "/dev/termination-log",
				TerminationMessagePolicy: "File",
				ImagePullPolicy:          "Always",
				SecurityContext: &k8sv1.SecurityContext{
					Privileged:               PointTo(false),
					RunAsUser:                PointTo(int64(150)),
					RunAsGroup:               PointTo(int64(150)),
					ReadOnlyRootFilesystem:   PointTo(true),
					AllowPrivilegeEscalation: PointTo(false),
				},
			}}
			Expect(deployment.Spec.Template.Spec.Containers).Should(Equal(expectedContainer))
			Expect(deployment.Spec.Template.Spec.ServiceAccountName).Should(Equal(appName))
			Expect(deployment.Spec.Template.Spec.PriorityClassName).Should(Equal("skip-medium"))
			Expect(deployment.ObjectMeta.Name).Should(Equal(appName))
			expectedSecurityContext := k8sv1.PodSecurityContext{
				FSGroup:            PointTo(int64(150)),
				SeccompProfile:     &k8sv1.SeccompProfile{Type: k8sv1.SeccompProfileType("RuntimeDefault")},
				SupplementalGroups: []int64{int64(150)},
			}
			Expect(deployment.Spec.Template.Spec.SecurityContext).Should(Equal(&expectedSecurityContext))

			By("Should have created a service account")
			sa := &corev1.ServiceAccount{
				ObjectMeta: application.ObjectMeta,
			}
			Eventually(func() bool {
				err := k8sClient.Get(ctx, client.ObjectKeyFromObject(sa), sa)
				return err == nil
			}, timeout, interval).Should(BeTrue())
			Expect(sa.Name).Should(Equal(appName))

			By("Should have created a service")
			service := &corev1.Service{
				ObjectMeta: application.ObjectMeta,
			}
			expectedService := &corev1.Service{
				ObjectMeta: application.ObjectMeta,
				Spec: corev1.ServiceSpec{
					Ports: []corev1.ServicePort{
						{
							Name:        "http",
							Protocol:    "TCP",
							AppProtocol: PointTo("http"),
							Port:        8080,
							TargetPort: intstr.IntOrString{
								IntVal: 8080,
							},
						},
					},
					Selector: map[string]string{
						"app": appName,
					},
				},
			}
			Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(service), service)).To(Succeed())
			Expect(mergo.Merge(&expectedService.Spec, &service.Spec)).To(Succeed())
			Expect(cmp.Equal(service.Spec, expectedService.Spec)).To(BeTrue())

			By("should have created an HPA")
			hpa := &autoscalingv2.HorizontalPodAutoscaler{
				ObjectMeta: application.ObjectMeta,
			}
			Eventually(func() bool {
				err := k8sClient.Get(ctx, client.ObjectKeyFromObject(hpa), hpa)
				return err == nil
			}, timeout, interval).Should(BeTrue())

			Expect(hpa.Spec.MinReplicas).Should(Equal(PointTo(int32(2))))
			Expect(hpa.Spec.MaxReplicas).Should(Equal(int32(5)))
			Expect(hpa.Spec.ScaleTargetRef.APIVersion).Should(Equal("apps/v1"))
			Expect(hpa.Spec.ScaleTargetRef.Kind).Should(Equal("Deployment"))
			Expect(hpa.Spec.ScaleTargetRef.Name).Should(Equal(appName))
			Expect(hpa.Spec.Metrics[0].Type).Should(Equal(autoscalingv2.MetricSourceType("Resource")))
			Expect(hpa.Spec.Metrics[0].Resource.Name).Should(Equal(k8sv1.ResourceName("cpu")))
			Expect(hpa.Spec.Metrics[0].Resource.Target.Type).Should(Equal(autoscalingv2.MetricTargetType("Utilization")))
			Expect(hpa.Spec.Metrics[0].Resource.Target.AverageUtilization).Should(Equal(PointTo(int32(80))))

			By("should have created a peerauthentication")
			pa := &v1beta1.PeerAuthentication{
				ObjectMeta: application.ObjectMeta,
			}

			Eventually(func() bool {
				err := k8sClient.Get(ctx, client.ObjectKeyFromObject(pa), pa)
				if err != nil {
					return false
				}
				return true
			}, timeout, interval).Should(BeTrue())

			Expect(pa.Spec.Selector.MatchLabels).Should(Equal(map[string]string{"app": appName}))
			Expect(pa.Spec.Mtls.Mode).Should(Equal(securityapi.PeerAuthentication_MutualTLS_STRICT))

			By("should have created an authorizationpolicy")
			ap := &v1beta1.AuthorizationPolicy{
				ObjectMeta: metav1.ObjectMeta{
					Namespace: application.Namespace,
					Name:      application.Name + "-deny",
				},
			}
			Eventually(func() bool {
				err := k8sClient.Get(ctx, client.ObjectKeyFromObject(ap), ap)
				if err != nil {
					return false
				}
				return true
			}, timeout, interval).Should(BeTrue())
			Expect(ap.Spec.Action).Should(Equal(securityapi.AuthorizationPolicy_DENY))
			Expect(ap.Spec.Rules[0].From[0].Source.Namespaces[0]).Should(Equal("istio-gateways"))
			Expect(ap.Spec.Rules[0].To[0].Operation.Paths[0]).Should(Equal("/actuator*"))
			Expect(ap.Spec.Selector.MatchLabels["app"]).Should(Equal(application.Name))
		})
	})

	Context("When an application has access policies", func() {
		It("should have created a network policy and service entry", func() {
			appName := "access-policy"
			appMinimal := "access-policy-two"
			appOtherMinimal := "access-policy-other"
			ns := newNamespace()
			otherNs := newNamespace()
			application = skiperatorv1alpha1.Application{
				ObjectMeta: metav1.ObjectMeta{
					Name:      appName,
					Namespace: ns.Name,
				},
				Spec: skiperatorv1alpha1.ApplicationSpec{
					Image: "image",
					Port:  8080,
				},
			}
			application.Namespace = ns.Name
			applicationMinimal := application
			applicationOtherMinimal := application
			applicationOtherMinimal.Name = appOtherMinimal
			applicationOtherMinimal.Namespace = otherNs.Name
			applicationMinimal.Name = appMinimal
			applicationMinimal.Namespace = ns.Name
			application.Name = appName

			accessPolicy := podtypes.AccessPolicy{
				Inbound: &podtypes.InboundPolicy{
					Rules: []podtypes.InternalRule{{
						Namespace:   otherNs.Name,
						Application: appOtherMinimal,
					}},
				},
				Outbound: podtypes.OutboundPolicy{
					Rules: []podtypes.InternalRule{{
						Namespace:   otherNs.Name,
						Application: appOtherMinimal,
					}, {
						Application: appMinimal,
					}},
					External: []podtypes.ExternalRule{{
						Host: "example.com",
						Ports: []podtypes.ExternalPort{{
							Name:     "http",
							Port:     80,
							Protocol: "HTTP",
						}}},
						{
							Host: "foo.com",
						}},
				},
			}
			application.Spec.AccessPolicy = &accessPolicy
			ctx := context.Background()
			Eventually(k8sClient.Create(ctx, &applicationMinimal)).ShouldNot(HaveOccurred())
			Eventually(k8sClient.Create(ctx, &applicationOtherMinimal)).ShouldNot(HaveOccurred())
			Eventually(k8sClient.Create(ctx, &application)).ShouldNot(HaveOccurred())

			By("Checking network policy")
			np := &v1.NetworkPolicy{
				ObjectMeta: metav1.ObjectMeta{
					Namespace: application.Namespace,
					Name:      appName,
				},
			}

			Eventually(func() bool {
				err := k8sClient.Get(ctx, client.ObjectKeyFromObject(np), np)
				return err == nil
			}, timeout, interval).Should(BeTrue())

			Expect(np.Spec.PodSelector.MatchLabels["app"]).Should(Equal(appName))
			Expect(np.Spec.Ingress[0].From[0].NamespaceSelector.MatchLabels["kubernetes.io/metadata.name"]).Should(Equal(otherNs.Name))
			Expect(np.Spec.Ingress[0].From[0].PodSelector.MatchLabels["app"]).Should(Equal(appOtherMinimal))
			Expect(np.Spec.Egress[0].To[0].NamespaceSelector.MatchLabels["kubernetes.io/metadata.name"]).Should(Equal(otherNs.Name))
			Expect(np.Spec.Egress[0].To[0].PodSelector.MatchLabels["app"]).Should(Equal(appOtherMinimal))
			Expect(&np.Spec.Egress[0].Ports[0].Port.IntVal).Should(Equal(PointTo(int32(8080))))
			Expect(np.Spec.Egress[1].To[0].NamespaceSelector.MatchLabels["kubernetes.io/metadata.name"]).Should(Equal(ns.Name))
			Expect(np.Spec.Egress[1].To[0].PodSelector.MatchLabels["app"]).Should(Equal(appMinimal))
			Expect(&np.Spec.Egress[1].Ports[0].Port.IntVal).Should(Equal(PointTo(int32(8080))))

			By("Checking service entry")
			se := &networkingv1beta1.ServiceEntry{
				ObjectMeta: metav1.ObjectMeta{
					Namespace: ns.Name,
				},
			}
			Eventually(func() bool {
				err := k8sClient.Get(ctx, client.ObjectKeyFromObject(se), se)
				return err == nil
			}, timeout, interval).Should(BeTrue())

		})
	})
})
