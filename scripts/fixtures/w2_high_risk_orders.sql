-- Install as the administrator of a fresh, disposable orders fixture cluster.
-- The fixture runner grants EXECUTE on these functions to the dedicated write
-- account after that account has passed the existing native provider check.
BEGIN;

CREATE ROLE mcpguard_w2_fixture_owner NOLOGIN NOSUPERUSER NOCREATEDB NOCREATEROLE NOINHERIT NOREPLICATION NOBYPASSRLS;
CREATE SCHEMA mcpguard_w2_fixture AUTHORIZATION mcpguard_w2_fixture_owner;
SET LOCAL ROLE mcpguard_w2_fixture_owner;
REVOKE ALL ON SCHEMA mcpguard_w2_fixture FROM PUBLIC;

CREATE TABLE mcpguard_w2_fixture.orders (
    tenant_id text NOT NULL,
    order_id text NOT NULL,
    status text NOT NULL CHECK (status IN ('READY_TO_SHIP', 'CANCELLED')),
    amount bigint NOT NULL CHECK (amount >= 0),
    version bigint NOT NULL CHECK (version > 0),
    shipping_code text NOT NULL,
    PRIMARY KEY (tenant_id, order_id)
);

INSERT INTO mcpguard_w2_fixture.orders VALUES
    ('tenant_a', 'order_recalc_1', 'READY_TO_SHIP', 100, 1, 'shipping_a'),
    ('tenant_a', 'order_cancelled_1', 'CANCELLED', 80, 1, 'shipping_b'),
    ('tenant_a', 'order_untouched_1', 'CANCELLED', 70, 1, 'shipping_c'),
    ('tenant_b', 'order_recalc_1', 'READY_TO_SHIP', 100, 1, 'shipping_d'),
    ('tenant_b', 'order_cancelled_1', 'CANCELLED', 80, 1, 'shipping_e'),
    ('tenant_b', 'order_other_tenant_1', 'READY_TO_SHIP', 50, 1, 'shipping_f');

CREATE FUNCTION mcpguard_w2_fixture.update_order_amount(target text, expected bigint)
RETURNS TABLE (outcome text, affected_rows integer, new_version bigint)
LANGUAGE plpgsql SECURITY DEFINER SET search_path = pg_catalog, pg_temp AS $$
DECLARE
    affected integer;
    current_version bigint;
BEGIN
    IF target IS DISTINCT FROM 'order_recalc_1' OR expected IS DISTINCT FROM 1 THEN
        RAISE EXCEPTION 'invalid fixture amount operation' USING ERRCODE = '22023';
    END IF;

    UPDATE mcpguard_w2_fixture.orders
       SET amount = 120, version = version + 1
     WHERE tenant_id = 'tenant_a' AND order_id = target AND version = expected
       AND status = 'READY_TO_SHIP' AND amount = 100;
    GET DIAGNOSTICS affected = ROW_COUNT;
    IF affected = 1 THEN
        RETURN QUERY SELECT 'UPDATED'::text, affected, expected + 1;
        RETURN;
    END IF;

    SELECT version INTO current_version FROM mcpguard_w2_fixture.orders
     WHERE tenant_id = 'tenant_a' AND order_id = target;
    RETURN QUERY SELECT CASE WHEN current_version IS NULL THEN 'NOT_FOUND'
                             WHEN current_version <> expected THEN 'VERSION_CONFLICT'
                             ELSE 'PRECONDITION_FAILED' END,
                        0, 0::bigint;
END;
$$;

CREATE FUNCTION mcpguard_w2_fixture.create_orders_index(target text)
RETURNS text LANGUAGE plpgsql SECURITY DEFINER SET search_path = pg_catalog, pg_temp AS $$
BEGIN
    IF target IS DISTINCT FROM 'orders_shipping_lookup_idx' THEN
        RAISE EXCEPTION 'invalid fixture index operation' USING ERRCODE = '22023';
    END IF;

    PERFORM pg_advisory_xact_lock(764602, 1);
    IF to_regclass('mcpguard_w2_fixture.orders_shipping_lookup_idx') IS NOT NULL THEN
        RETURN 'INDEX_EXISTS';
    END IF;

    CREATE INDEX orders_shipping_lookup_idx ON mcpguard_w2_fixture.orders USING btree (shipping_code);
    RETURN 'INDEX_CREATED';
END;
$$;

CREATE FUNCTION mcpguard_w2_fixture.delete_cancelled_order(target text, expected bigint)
RETURNS TABLE (outcome text, affected_rows integer)
LANGUAGE plpgsql SECURITY DEFINER SET search_path = pg_catalog, pg_temp AS $$
DECLARE
    affected integer;
    current_version bigint;
BEGIN
    IF target IS DISTINCT FROM 'order_cancelled_1' OR expected IS DISTINCT FROM 1 THEN
        RAISE EXCEPTION 'invalid fixture delete operation' USING ERRCODE = '22023';
    END IF;

    DELETE FROM mcpguard_w2_fixture.orders
     WHERE tenant_id = 'tenant_a' AND order_id = target AND version = expected AND status = 'CANCELLED';
    GET DIAGNOSTICS affected = ROW_COUNT;
    IF affected = 1 THEN
        RETURN QUERY SELECT 'DELETED'::text, affected;
        RETURN;
    END IF;

    SELECT version INTO current_version FROM mcpguard_w2_fixture.orders
     WHERE tenant_id = 'tenant_a' AND order_id = target;
    RETURN QUERY SELECT CASE WHEN current_version IS NULL THEN 'NOT_FOUND'
                             WHEN current_version <> expected THEN 'VERSION_CONFLICT'
                             ELSE 'PRECONDITION_FAILED' END,
                        0;
END;
$$;

REVOKE ALL ON FUNCTION mcpguard_w2_fixture.update_order_amount(text,bigint),
    mcpguard_w2_fixture.create_orders_index(text),
    mcpguard_w2_fixture.delete_cancelled_order(text,bigint) FROM PUBLIC;
COMMIT;
