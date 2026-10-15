CREATE PROCEDURE acme.stage_lines
AS
BEGIN
    DROP TABLE IF EXISTS #work;

    CREATE TABLE #work (
        line_id INT NOT NULL PRIMARY KEY,
        label NVARCHAR(50) NULL
    );

    INSERT INTO #work (line_id, label)
    SELECT line_id, sku FROM acme.order_lines;

    DROP TABLE #work;
END
