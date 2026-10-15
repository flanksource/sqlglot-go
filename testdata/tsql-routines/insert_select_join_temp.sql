CREATE PROCEDURE acme.copy_lines
AS
BEGIN
    CREATE TABLE #map (src INT NOT NULL, dst INT NOT NULL);

    INSERT INTO #map (src, dst)
    SELECT line_id, line_id + 1000 FROM acme.order_lines;

    INSERT acme.order_lines (line_id, order_id, sku, quantity)
    SELECT m.dst, l.order_id, l.sku, l.quantity
    FROM acme.order_lines AS l
    INNER JOIN #map AS m ON m.src = l.line_id
    LEFT JOIN acme.orders AS o ON o.order_id = l.order_id
    WHERE o.status <> N'closed';
END
