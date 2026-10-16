# The interpreter: a loop of arithmetic and list operations.
total = 0
items = []
for number in range(3000000):
    total += number * number % 7
    if number % 1000 == 0:
        items.append(total)
print(total, len(items))
