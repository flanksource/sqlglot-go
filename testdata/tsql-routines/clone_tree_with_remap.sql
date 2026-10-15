-- Copies one order with its lines and each line's notes, three levels deep,
-- remapping every key through #map and reporting the rows copied per table.
CREATE OR ALTER PROCEDURE acme.clone_order
    @source_order_id UNIQUEIDENTIFIER,
    @new_order_id UNIQUEIDENTIFIER OUTPUT
AS
BEGIN
    SET NOCOUNT ON;
    SET XACT_ABORT ON;

    DECLARE @orders INT, @lines INT, @notes INT;

    IF NOT EXISTS (SELECT 1 FROM acme.orders WHERE order_id = @source_order_id)
        THROW 50001, N'source order not found', 1;

    CREATE TABLE #map (src UNIQUEIDENTIFIER NOT NULL PRIMARY KEY, dst UNIQUEIDENTIFIER NOT NULL);

    SET @new_order_id = NEWID();
    INSERT INTO #map (src, dst) VALUES (@source_order_id, @new_order_id);

    INSERT INTO acme.orders (order_id, customer_id, status, created_at)
    SELECT m.dst, o.customer_id, N'draft', SYSUTCDATETIME()
    FROM acme.orders AS o
    JOIN #map AS m ON m.src = o.order_id;
    SET @orders = @@ROWCOUNT;

    INSERT INTO #map (src, dst)
    SELECT l.line_id, NEWID()
    FROM acme.order_lines AS l
    WHERE l.order_id = @source_order_id;

    INSERT INTO acme.order_lines (line_id, order_id, sku, quantity)
    SELECT ml.dst, mo.dst, l.sku, l.quantity
    FROM acme.order_lines AS l
    JOIN #map AS ml ON ml.src = l.line_id
    JOIN #map AS mo ON mo.src = l.order_id;
    SELECT @lines = @@ROWCOUNT;

    INSERT INTO #map (src, dst)
    SELECT n.note_id, NEWID()
    FROM acme.line_notes AS n
    JOIN acme.order_lines AS l ON l.line_id = n.line_id
    WHERE l.order_id = @source_order_id;

    INSERT INTO acme.line_notes (note_id, line_id, body)
    SELECT mn.dst, ml.dst, n.body
    FROM acme.line_notes AS n
    JOIN #map AS mn ON mn.src = n.note_id
    JOIN #map AS ml ON ml.src = n.line_id;
    SET @notes = @@ROWCOUNT;

    DROP TABLE #map;

    SELECT N'orders' AS TableName, @orders AS Rows
    UNION ALL
    SELECT N'order_lines', @lines
    UNION ALL
    SELECT N'line_notes', @notes;
END
GO
