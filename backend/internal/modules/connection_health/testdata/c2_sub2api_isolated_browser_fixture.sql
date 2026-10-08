-- Local Sub2API browser fixture. The project-local safe runner supplies saved
-- IDs, original-row fingerprints and the current read-only C3 blacklist.
-- Never point this fixture at another database or invoke it with arbitrary IDs.
\set ON_ERROR_STOP on
\set QUIET on
BEGIN;
SET LOCAL search_path=public,pg_temp;
SET LOCAL lock_timeout='5s';
SET LOCAL statement_timeout='15s';
SELECT set_config('c2.fixture_action', :'fixture_action', true) AS action_setting,
       set_config('c2.fixture_marker', 'C2 20261008 isolated browser fixture', true) AS marker_setting,
       set_config('c2.fixture_ids', :'fixture_ids', true) AS ids_setting,
       set_config('c2.fixture_fingerprints', :'fixture_fingerprints', true) AS fingerprints_setting,
       set_config('c2.fixture_c3', :'fixture_c3', true) AS c3_setting
\gset
SELECT pg_advisory_xact_lock(hashtextextended('C2 20261008 isolated browser fixture', 0))
\gset
LOCK TABLE accounts, groups, account_groups IN SHARE ROW EXCLUSIVE MODE;

CREATE FUNCTION pg_temp.c2_original_fingerprints(account_ids bigint[], group_ids bigint[])
RETURNS jsonb LANGUAGE sql AS $body$
SELECT jsonb_build_object(
    'accounts', (SELECT md5(COALESCE(string_agg(md5(to_jsonb(a)::text), ',' ORDER BY a.id), '')) FROM accounts a WHERE NOT a.id=ANY(account_ids)),
    'groups', (SELECT md5(COALESCE(string_agg(md5(to_jsonb(g)::text), ',' ORDER BY g.id), '')) FROM groups g WHERE NOT g.id=ANY(group_ids)),
    'memberships', (SELECT md5(COALESCE(string_agg(md5(to_jsonb(m)::text), ',' ORDER BY m.account_id,m.group_id), '')) FROM account_groups m WHERE NOT (m.account_id=ANY(account_ids) AND m.group_id=ANY(group_ids)))
);
$body$;

-- Cascading or SET NULL foreign keys are also refused: they must never erase
-- or change external rows as a side effect of removing the fixture.
CREATE FUNCTION pg_temp.c2_assert_no_external_references(parent_table regclass, fixture_ids bigint[])
RETURNS void LANGUAGE plpgsql AS $body$
DECLARE reference record; referenced boolean;
BEGIN
    IF cardinality(fixture_ids)=0 THEN RETURN; END IF;
    FOR reference IN
        SELECT c.conrelid, c.conkey, c.confkey,
               (SELECT a.attname FROM pg_attribute a WHERE a.attrelid=c.conrelid AND a.attnum=c.conkey[1]) AS column_name,
               (SELECT a.attname FROM pg_attribute a WHERE a.attrelid=c.confrelid AND a.attnum=c.confkey[1]) AS parent_column
        FROM pg_constraint c
        WHERE c.contype='f' AND c.confrelid=parent_table
          AND c.conrelid<>'public.account_groups'::regclass
    LOOP
        IF cardinality(reference.conkey)<>1 OR cardinality(reference.confkey)<>1 OR reference.parent_column<>'id' THEN
            RAISE EXCEPTION 'c2_fixture_unsupported_foreign_key';
        END IF;
        EXECUTE format('SELECT EXISTS(SELECT 1 FROM %s WHERE %I=ANY($1))',reference.conrelid::regclass,reference.column_name)
        INTO referenced USING fixture_ids;
        IF referenced THEN RAISE EXCEPTION 'c2_fixture_external_foreign_key_reference'; END IF;
    END LOOP;
END;
$body$;

DO $body$
DECLARE
    action text := current_setting('c2.fixture_action');
    marker text := current_setting('c2.fixture_marker');
    ids jsonb := current_setting('c2.fixture_ids')::jsonb;
    fingerprints jsonb := current_setting('c2.fixture_fingerprints')::jsonb;
    c3 jsonb := current_setting('c2.fixture_c3')::jsonb;
    a bigint := COALESCE((ids->>'accountA')::bigint,0);
    b bigint := COALESCE((ids->>'accountB')::bigint,0);
    g1 bigint := COALESCE((ids->>'group1')::bigint,0);
    g2 bigint := COALESCE((ids->>'group2')::bigint,0);
    accounts_ids bigint[];
    groups_ids bigint[];
    affected integer;
BEGIN
    IF current_database()<>'sub2api' OR current_user<>'sub2api' OR inet_server_addr() IS NOT NULL THEN
        RAISE EXCEPTION 'c2_fixture_wrong_database_identity';
    END IF;
    IF action NOT IN ('prepare','counts','moveA','clearA','restore','dropA','dropB','dropGroup1','cleanup') THEN
        RAISE EXCEPTION 'c2_fixture_invalid_action';
    END IF;
    IF action='prepare' THEN
        IF a<>0 OR b<>0 OR g1<>0 OR g2<>0 THEN RAISE EXCEPTION 'c2_fixture_prepare_requires_empty_ids'; END IF;
        IF EXISTS(SELECT 1 FROM accounts WHERE notes=marker OR name LIKE marker||'%')
           OR EXISTS(SELECT 1 FROM groups WHERE description=marker OR name LIKE marker||'%') THEN
            RAISE EXCEPTION 'c2_fixture_marker_collision';
        END IF;
        IF pg_temp.c2_original_fingerprints(ARRAY[]::bigint[],ARRAY[]::bigint[]) IS DISTINCT FROM fingerprints THEN
            RAISE EXCEPTION 'c2_fixture_original_rows_changed';
        END IF;
        INSERT INTO groups(name,description,platform,status,is_exclusive)
        VALUES(marker||' Group 1',marker,'openai','active',false) RETURNING id INTO g1;
        INSERT INTO groups(name,description,platform,status,is_exclusive)
        VALUES(marker||' Group 2',marker,'openai','active',false) RETURNING id INTO g2;
        INSERT INTO accounts(name,platform,type,credentials,extra,status,schedulable,notes,priority)
        VALUES(marker||' Account A','openai','apikey',
               jsonb_build_object('api_key','sk-c2-isolated-browser-fixture-A','base_url','http://127.0.0.1:18777'),
               jsonb_build_object('c2_fixture_marker',marker,'openai_long_context_billing_enabled',false),
               'active',false,marker,100) RETURNING id INTO a;
        INSERT INTO accounts(name,platform,type,credentials,extra,status,schedulable,notes,priority)
        VALUES(marker||' Account B','openai','apikey',
               jsonb_build_object('api_key','sk-c2-isolated-browser-fixture-B','base_url','http://127.0.0.1:18777'),
               jsonb_build_object('c2_fixture_marker',marker,'openai_long_context_billing_enabled',false),
               'inactive',false,marker,100) RETURNING id INTO b;
        INSERT INTO account_groups(account_id,group_id,priority) VALUES(a,g1,100),(b,g1,100),(a,g2,100);
    ELSIF a<=0 OR b<=0 OR g1<=0 OR g2<=0 OR a=b OR g1=g2 THEN
        RAISE EXCEPTION 'c2_fixture_invalid_saved_ids';
    END IF;
    accounts_ids := ARRAY[a,b]; groups_ids := ARRAY[g1,g2];

    IF EXISTS(SELECT 1 FROM jsonb_array_elements_text(c3->'accountIds') x(id) WHERE x.id IN(a::text,b::text))
       OR EXISTS(SELECT 1 FROM jsonb_array_elements_text(c3->'groupIds') x(id) WHERE x.id IN(g1::text,g2::text)) THEN
        RAISE EXCEPTION 'c2_fixture_existing_c3_reference';
    END IF;
    PERFORM id FROM accounts WHERE id=ANY(accounts_ids) ORDER BY id FOR UPDATE;
    PERFORM id FROM groups WHERE id=ANY(groups_ids) ORDER BY id FOR UPDATE;
    IF EXISTS(SELECT 1 FROM accounts WHERE (notes=marker OR name LIKE marker||'%') AND NOT id=ANY(accounts_ids))
       OR EXISTS(SELECT 1 FROM groups WHERE (description=marker OR name LIKE marker||'%') AND NOT id=ANY(groups_ids)) THEN
        RAISE EXCEPTION 'c2_fixture_marker_collision';
    END IF;
    IF EXISTS(
        SELECT 1 FROM accounts WHERE id=ANY(accounts_ids) AND (
            name IS DISTINCT FROM marker||CASE WHEN id=a THEN ' Account A' ELSE ' Account B' END
            OR notes IS DISTINCT FROM marker OR platform<>'openai' OR type<>'apikey'
            OR credentials IS DISTINCT FROM jsonb_build_object('api_key',CASE WHEN id=a THEN 'sk-c2-isolated-browser-fixture-A' ELSE 'sk-c2-isolated-browser-fixture-B' END,'base_url','http://127.0.0.1:18777')
            OR status IS DISTINCT FROM CASE WHEN id=a THEN 'active' ELSE 'inactive' END
            OR schedulable OR proxy_id IS NOT NULL OR parent_account_id IS NOT NULL OR deleted_at IS NOT NULL
            OR extra->>'c2_fixture_marker' IS DISTINCT FROM marker
        )
    ) OR EXISTS(
        SELECT 1 FROM groups WHERE id=ANY(groups_ids) AND (
            name IS DISTINCT FROM marker||CASE WHEN id=g1 THEN ' Group 1' ELSE ' Group 2' END
            OR description IS DISTINCT FROM marker OR platform<>'openai' OR status<>'active' OR is_exclusive OR deleted_at IS NOT NULL
        )
    ) THEN RAISE EXCEPTION 'c2_fixture_saved_identity_changed'; END IF;
    IF EXISTS(SELECT 1 FROM account_groups WHERE
         (account_id=ANY(accounts_ids) OR group_id=ANY(groups_ids))
         AND NOT(account_id=ANY(accounts_ids) AND group_id=ANY(groups_ids))) THEN
        RAISE EXCEPTION 'c2_fixture_external_membership_reference';
    END IF;
    IF pg_temp.c2_original_fingerprints(accounts_ids,groups_ids) IS DISTINCT FROM fingerprints THEN
        RAISE EXCEPTION 'c2_fixture_original_rows_changed';
    END IF;

    IF action IN ('moveA','clearA','restore') THEN
        IF NOT EXISTS(SELECT 1 FROM accounts WHERE id=a) THEN RAISE EXCEPTION 'c2_fixture_account_a_missing'; END IF;
        IF action IN ('moveA','restore') AND NOT EXISTS(SELECT 1 FROM groups WHERE id=g2) THEN
            RAISE EXCEPTION 'c2_fixture_group_2_missing';
        END IF;
        DELETE FROM account_groups WHERE account_id=a AND group_id=ANY(groups_ids);
        IF action='moveA' THEN
            INSERT INTO account_groups(account_id,group_id,priority) VALUES(a,g2,100);
        ELSIF action='restore' THEN
            IF NOT EXISTS(SELECT 1 FROM accounts WHERE id=b) OR NOT EXISTS(SELECT 1 FROM groups WHERE id=g1) THEN
                RAISE EXCEPTION 'c2_fixture_restore_parent_missing';
            END IF;
            INSERT INTO account_groups(account_id,group_id,priority) VALUES(a,g1,100),(a,g2,100);
            INSERT INTO account_groups(account_id,group_id,priority) VALUES(b,g1,100) ON CONFLICT DO NOTHING;
        END IF;
    ELSIF action IN ('dropA','dropB') THEN
        PERFORM pg_temp.c2_assert_no_external_references('public.accounts'::regclass,ARRAY[CASE WHEN action='dropA' THEN a ELSE b END]);
        DELETE FROM account_groups WHERE account_id=CASE WHEN action='dropA' THEN a ELSE b END AND group_id=ANY(groups_ids);
        DELETE FROM accounts WHERE id=CASE WHEN action='dropA' THEN a ELSE b END AND notes=marker;
    ELSIF action='dropGroup1' THEN
        PERFORM pg_temp.c2_assert_no_external_references('public.groups'::regclass,ARRAY[g1]);
        DELETE FROM account_groups WHERE group_id=g1 AND account_id=ANY(accounts_ids);
        DELETE FROM groups WHERE id=g1 AND description=marker;
    ELSIF action='cleanup' THEN
        -- Original data has already been checked above, before any deletion.
        PERFORM pg_temp.c2_assert_no_external_references('public.accounts'::regclass,accounts_ids);
        PERFORM pg_temp.c2_assert_no_external_references('public.groups'::regclass,groups_ids);
        DELETE FROM account_groups WHERE account_id=ANY(accounts_ids) AND group_id=ANY(groups_ids);
        DELETE FROM accounts WHERE id=ANY(accounts_ids) AND notes=marker;
        DELETE FROM groups WHERE id=ANY(groups_ids) AND description=marker;
    END IF;
    IF pg_temp.c2_original_fingerprints(accounts_ids,groups_ids) IS DISTINCT FROM fingerprints THEN
        RAISE EXCEPTION 'c2_fixture_original_rows_changed';
    END IF;
    PERFORM set_config('c2.fixture_ids',jsonb_build_object('accountA',a,'accountB',b,'group1',g1,'group2',g2)::text,true);
END;
$body$;

SELECT jsonb_build_object(
    'ids',current_setting('c2.fixture_ids')::jsonb,
    'counts',jsonb_build_object(
        'accounts',(SELECT count(*) FROM accounts),'liveAccounts',(SELECT count(*) FROM accounts WHERE deleted_at IS NULL),
        'groups',(SELECT count(*) FROM groups),'liveGroups',(SELECT count(*) FROM groups WHERE deleted_at IS NULL),
        'memberships',(SELECT count(*) FROM account_groups),
        'c2Accounts',(SELECT count(*) FROM accounts WHERE notes=current_setting('c2.fixture_marker')),
        'c2Groups',(SELECT count(*) FROM groups WHERE description=current_setting('c2.fixture_marker'))
    )
);
COMMIT;
