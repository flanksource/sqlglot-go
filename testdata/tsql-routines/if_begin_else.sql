CREATE PROCEDURE acme.classify_order
    @order_id INT,
    @label NVARCHAR(20) OUTPUT
AS
BEGIN
    DECLARE @total DECIMAL(18, 2) = (SELECT SUM(amount) FROM acme.order_lines WHERE order_id = @order_id);

    IF @total IS NULL
    BEGIN
        SET @label = N'empty';
        RETURN;
    END
    ELSE IF @total > 1000
        SET @label = N'large';
    ELSE
    BEGIN
        IF @total < 10 SET @label = N'tiny' ELSE SET @label = N'normal'
    END
END
