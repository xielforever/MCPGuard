import hudson.model.BooleanParameterDefinition
import hudson.model.ChoiceParameterDefinition
import hudson.model.FreeStyleProject
import hudson.model.ParametersDefinitionProperty
import hudson.model.StringParameterDefinition
import hudson.security.FullControlOnceLoggedInAuthorizationStrategy
import hudson.security.HudsonPrivateSecurityRealm
import jenkins.model.Jenkins
import jenkins.model.JenkinsLocationConfiguration
import jenkins.security.ApiTokenProperty

// This file is loaded only by the disposable, loopback-only acceptance controller.
def jenkins = Jenkins.get()
def realm = new HudsonPrivateSecurityRealm(false)
jenkins.setSecurityRealm(realm)
def authorization = new FullControlOnceLoggedInAuthorizationStrategy()
authorization.setAllowAnonymousRead(false)
jenkins.setAuthorizationStrategy(authorization)

[admin: 'MCPGUARD_FIXTURE_ADMIN_TOKEN', gateway: 'MCPGUARD_FIXTURE_GATEWAY_TOKEN'].each { name, tokenEnv ->
    def user = realm.createAccount(name, UUID.randomUUID().toString())
    user.getProperty(ApiTokenProperty.class).addFixedNewToken('acceptance fixture', System.getenv(tokenEnv))
}

JenkinsLocationConfiguration.get().setUrl(System.getenv('MCPGUARD_FIXTURE_PROXY_URL') + '/')
jenkins.setNumExecutors(1)
jenkins.setQuietPeriod(2)

// These jobs exercise Jenkins' native parameter and queue APIs. Neither has build steps.
def orders = jenkins.createProject(FreeStyleProject.class, 'orders-validation')
orders.addProperty(new ParametersDefinitionProperty(
    new StringParameterDefinition('GIT_REF', 'main', 'Reference to validate'),
    new BooleanParameterDefinition('RUN_TESTS', true, 'Run integration checks')
))
def inventory = jenkins.createProject(FreeStyleProject.class, 'inventory-verification')
inventory.addProperty(new ParametersDefinitionProperty(
    new ChoiceParameterDefinition('PROFILE', ['fast', 'full'] as String[], 'Verification profile'),
    new StringParameterDefinition('RELEASE_TAG', 'candidate', 'Release tag')
))
jenkins.save()
