CREATE PROCEDURE acme.list_orders
    @status NVARCHAR(20) = N'open',
    @limit INT = 100,
    @since DATE = NULL,
    @ratio DECIMAL(5, 2) = 0.5,
    @ids acme.id_list READONLY
AS
SELECT TOP (@limit) o.order_id
FROM acme.orders AS o
WHERE o.status = @status
  AND (@since IS NULL OR o.created_at >= @since)
  AND o.order_id IN (SELECT id FROM @ids)
