import copy,itertools,unittest
from app import order_jobs
class Tests(unittest.TestCase):
    def test_new_ready_job_precedes_old_ready_job(self):
        graph={'z':[],'a':['b'],'b':[]}
        self.assertEqual(order_jobs(graph),['b','a','z'])
    def test_duplicate_edges_and_immutability(self):
        graph={'a':['b','b'],'b':[],'c':['a','b']}
        before=copy.deepcopy(graph)
        self.assertEqual(order_jobs(graph),['b','a','c'])
        self.assertEqual(graph,before)
        self.assertEqual(order_jobs({}),[])
    def test_invalid_graphs(self):
        for graph in [{'a':['missing']},{'a':['a']},{'a':['b'],'b':['a']},{'z':[],'a':['b'],'b':['a']}]:
            before=copy.deepcopy(graph)
            with self.subTest(graph=graph),self.assertRaises(ValueError): order_jobs(graph)
            self.assertEqual(graph,before)
    def test_all_small_forward_edge_graphs(self):
        names=['c','a','d','b']
        edges=[(names[j],names[i]) for i in range(4) for j in range(i+1,4)]
        for bits in itertools.product([False,True],repeat=len(edges)):
            graph={n:[] for n in reversed(names)}
            for enabled,(job,need) in zip(bits,edges):
                if enabled: graph[job].append(need)
            valid=[p for p in itertools.permutations(names) if all(p.index(need)<p.index(job) for job,needs in graph.items() for need in needs)]
            expected=list(min(valid))
            with self.subTest(bits=bits): self.assertEqual(order_jobs(graph),expected)
