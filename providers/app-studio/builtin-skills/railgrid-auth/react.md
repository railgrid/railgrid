# React sign-in state

React calls only the app's same-origin API. It never imports the Node SDK or
reads a Railgrid assertion from browser storage.

```tsx
const loginHref = '/__railgrid/auth/login?returnTo=/';

async function loadCurrentUser() {
  const response = await fetch('/api/me', { credentials: 'same-origin' });
  if (response.status === 401) return { kind: 'signed-out' as const };
  if (!response.ok) throw new Error('Could not load the current user');
  return { kind: 'signed-in' as const, user: await response.json() };
}

function SignInLink() {
  return <a href={loginHref}>Sign in</a>;
}

async function signOut() {
  const response = await fetch('/__railgrid/auth/logout', {
    method: 'POST',
    credentials: 'same-origin',
  });
  if (!response.ok) throw new Error('Could not sign out');
}
```

When any app API returns `401`, clear the in-memory user view and offer the
sign-in link. Do not redirect every API failure: show the expired-session state
so the user can choose when to sign in again. The browser never receives the
signed assertion. Logout is a same-origin POST and the platform checks the
request origin.
