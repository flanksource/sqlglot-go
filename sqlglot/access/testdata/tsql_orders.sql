-- Order procedures of the demo shop, as SQL Server stores them.
CREATE OR ALTER PROCEDURE dbo.SubmitOrder
    @OrderId int,
    @CustomerId int
AS
BEGIN
    SET NOCOUNT ON;
    IF EXISTS (SELECT 1 FROM dbo.Customers WHERE CustomerId = @CustomerId)
    BEGIN
        EXEC dbo.ValidateOrder @OrderId = @OrderId;
        INSERT INTO dbo.Orders (OrderId, CustomerId, Status)
        VALUES (@OrderId, @CustomerId, 'submitted');
    END
    ELSE
        EXEC dbo.RejectOrder @OrderId;
    WHILE EXISTS (SELECT 1 FROM dbo.OrderLines WHERE OrderId = @OrderId AND Reserved = 0)
        UPDATE dbo.OrderLines SET Reserved = 1 WHERE OrderId = @OrderId;
END
GO

CREATE PROCEDURE [dbo].[ValidateOrder] @OrderId int
AS
    SELECT o.OrderId, c.Name, dbo.OrderTotal(o.OrderId) AS Total
    FROM dbo.Orders o
    JOIN dbo.Customers c ON c.CustomerId = o.CustomerId
    WHERE o.OrderId = @OrderId;
GO

CREATE PROCEDURE dbo.RejectOrder @OrderId int
AS
    WITH Rejected AS (SELECT OrderId FROM dbo.Orders WHERE OrderId = @OrderId)
    SELECT OrderId INTO dbo.RejectedOrders FROM Rejected;
GO

CREATE FUNCTION dbo.OrderTotal (@OrderId int) RETURNS decimal(18, 2)
AS
BEGIN
    RETURN (SELECT SUM(Amount) FROM dbo.OrderLines WHERE OrderId = @OrderId);
END
GO

CREATE PROCEDURE dbo.Ping
AS
    SELECT 1 AS Pong;
GO
