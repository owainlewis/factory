class Cache:
    def __init__(self, capacity, clock):
        self.capacity=capacity
        self.clock=clock
        self.values={}
    def put(self,key,value,ttl):
        self.values[key]=value
    def get(self,key):
        return self.values[key]
