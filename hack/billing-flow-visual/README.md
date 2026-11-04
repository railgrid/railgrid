# Billing flow browser fixture

This runs the unchanged production portal entry point, Vue Router and context
guards, AppLayout, TenantSettingsPage, OrgBillingCard, typography and PortalKit.
No component or settings template is substituted. Only API replies, a synthetic
session, and hosted payment pages are fixtures.

Run `make serve-billing-flow-visual`, then open:

<http://127.0.0.1:5198/ui/11111111-1111-4111-8111-111111111111/22222222-2222-4222-8222-222222222222/settings/organizations?panel=billing>

Billing lives under Organization settings → Plan & billing, with a shortcut
in the existing account menu. General remains the real organization settings
content. The fixture toolbar selects pricing, error and permission states.
Workspace creation and quota recovery use the production picker and create
dialog; fixture checkout redirects to the real return route, where activation
polling reads changing entitlement snapshots. No real payment is collected.

Free/Team/Enterprise allowance fixtures follow the published marketing tiers (3/25/100 workspaces). Demo and
My Workspace match the onboarding fixture. $50 USD/month is a sample API price,
explicitly marked outside product UI. Production pricing is supplied by the
billing provider, not by this fixture or a portal constant.

Unknown writes return 405; simulated Workspace creation remains in memory.
No upstream hub or payment service is contacted. The preview seeds a synthetic
session only at its dedicated loopback origin. BILLING_PREVIEW_PORT selects
the port. Stop the serving process to stop the preview.

With the server running, `make test-billing-flow-visual` checks the production
flow and captures desktop/mobile screens. Supply PLAYWRIGHT_MODULE and
CHROMIUM_EXECUTABLE for an existing browser installation; BILLING_PREVIEW_OUTPUT
selects the capture directory. `make test-billing` runs hub and portal unit tests.

The plan chooser reuses the marketing pricing hierarchy and descriptions. Its
amounts, currencies and effective allowances come from API responses. The
preview has its own Vite cache so it can run alongside the Tilt portal. Both
caches live under `portal/.vite-cache/`, outside `node_modules`, so a production
build’s dependency reinstall does not remove modules still served by Tilt.
