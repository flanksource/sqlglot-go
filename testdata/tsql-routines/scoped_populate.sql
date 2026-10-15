CREATE OR ALTER PROCEDURE acme.populate_region_totals
    @region_ids NVARCHAR(MAX),
    @dry_run BIT = 0
AS
BEGIN
    SET NOCOUNT ON;

    DECLARE @scope TABLE (region_id INT NOT NULL PRIMARY KEY);

    INSERT INTO @scope (region_id)
    SELECT DISTINCT CAST(s.value AS INT)
    FROM STRING_SPLIT(@region_ids, N',') AS s;

    IF @dry_run = 1
    BEGIN
        SELECT o.region_id, COUNT(*) AS orders
        FROM acme.orders AS o
        JOIN @scope AS sc ON sc.region_id = o.region_id
        GROUP BY o.region_id
        OPTION (RECOMPILE);
        RETURN;
    END
    ELSE
    BEGIN
        BEGIN
            DELETE FROM acme.region_totals
            WHERE region_id IN (SELECT ids.id FROM acme.split_ids(@region_ids) AS ids);

            INSERT INTO acme.region_totals (region_id, total)
            SELECT o.region_id, SUM(o.amount)
            FROM acme.orders AS o
            JOIN acme.split_ids(@region_ids) AS ids ON ids.id = o.region_id
            GROUP BY o.region_id
            OPTION (RECOMPILE);
        END
    END
END
