import copy
import math
from collections import OrderedDict

class Cache:
    def __init__(self, capacity, clock):
        if isinstance(capacity,bool) or not isinstance(capacity,int) or capacity<=0:
            raise ValueError('invalid capacity')
        self.capacity=capacity
        self.clock=clock
        self.values=OrderedDict()
    def _purge(self, now):
        for key in list(self.values):
            if now>=self.values[key][1]:
                del self.values[key]
    def put(self,key,value,ttl):
        if isinstance(ttl,bool) or not isinstance(ttl,(int,float)) or not math.isfinite(ttl):
            raise ValueError('invalid ttl')
        now=self.clock()
        self._purge(now)
        self.values.pop(key,None)
        if ttl<=0:
            return
        self.values[key]=(copy.deepcopy(value),now+ttl)
        while len(self.values)>self.capacity:
            self.values.popitem(last=False)
    def get(self,key):
        self._purge(self.clock())
        value,deadline=self.values[key]
        self.values.move_to_end(key)
        return copy.deepcopy(value)
