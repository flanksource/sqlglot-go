CREATE PROCEDURE acme.snapshot_orders
    @since DATETIME2(7)
AS
BEGIN
    SELECT o.order_id, o.customer_id, o.created_at
    INTO #recent
    FROM acme.orders AS o
    WHERE o.created_at >= @since;

    SELECT COUNT(*) AS recent_orders FROM #recent;

    DROP TABLE #recent;
END
