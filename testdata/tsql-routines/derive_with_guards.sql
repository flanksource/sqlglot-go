CREATE PROCEDURE tenant_x.derive_grants
    @tenant_id INT,
    @grant_role NVARCHAR(50) = N'reader'
AS
BEGIN
    SET NOCOUNT ON;

    -- Idempotent: a tenant whose grants are already derived is left alone.
    IF EXISTS (SELECT 1 FROM tenant_x.grants WHERE tenant_id = @tenant_id AND grant_role = @grant_role AND derived = 1)
        RETURN;

    DECLARE @dupes INT;
    DECLARE @message NVARCHAR(200);

    SELECT @dupes = COUNT(*)
    FROM (
        SELECT g.user_id
        FROM tenant_x.grants AS g
        WHERE g.tenant_id = @tenant_id
          AND g.user_id IN (
              SELECT m.user_id
              FROM tenant_x.members AS m
              WHERE m.team_id IN (SELECT t.team_id FROM tenant_x.teams AS t WHERE t.tenant_id = @tenant_id)
          )
        GROUP BY g.user_id
        HAVING COUNT(*) > 1
    ) AS d;

    IF @dupes > 0
    BEGIN
        SET @message = CONCAT(N'duplicate grants for tenant ', @tenant_id, N': ', @dupes);
        THROW 50002, @message, 1;
    END

    INSERT INTO tenant_x.grants (tenant_id, user_id, grant_role, derived)
    SELECT @tenant_id, m.user_id, @grant_role, 1
    FROM tenant_x.members AS m
    WHERE m.team_id IN (SELECT t.team_id FROM tenant_x.teams AS t WHERE t.tenant_id = @tenant_id);
END
