# User-owned SQL data

Treat the Railgrid `user.id` as an opaque, stable owner key. Keep names for
display only and do not make email a primary or foreign key.

```sql
CREATE TABLE app_users (
  railgrid_user_id TEXT PRIMARY KEY,
  display_name TEXT,
  created_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE TABLE orders (
  id UUID PRIMARY KEY,
  owner_railgrid_user_id TEXT NOT NULL REFERENCES app_users(railgrid_user_id),
  payload JSONB NOT NULL,
  created_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE INDEX orders_by_owner_created
  ON orders (owner_railgrid_user_id, created_at DESC);
```

For example, Alice's request uses Alice's verified opaque ID and Bob's uses
Bob's. Keep that ID in the server query parameter:

```sql
-- Alice can see only Alice's rows.
SELECT id, payload, created_at
  FROM orders
 WHERE owner_railgrid_user_id = $1;
-- $1 is user.id from the verified request, for example 'rg-user-alice'.

-- Bob's separate request binds 'rg-user-bob' and receives only Bob's rows.
```

For each request, take `user.id` from verified middleware and use it in every
read and write predicate. For example, load orders with
`WHERE owner_railgrid_user_id = $1`, where `$1` is the verified ID. Never accept
the owner ID from JSON, a URL parameter, or a hidden form field.

Two users with different IDs must see and change only their own rows. An update
should include both the row ID and owner ID in its `WHERE` clause. If the app
has shared records, define and review a separate sharing policy before changing
the ownership predicate.
